package relay

import (
	"testing"

	"cursor-client/internal/database"
)

// TestGatewayCreateConversationAndTurn tests the Gateway integration for conversation and turn creation
func TestGatewayCreateConversationAndTurn(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{
		db: db,
	}

	chatReq := unifiedChatRequest{
		RequestID:     "test-req-1",
		WorkspaceRoot: "/test",
		ModelName:     "test-model",
		AgentMode:     cursorAgentModeAgent,
		Messages: []chatMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	state := &agentSessionState{
		Messages: []chatMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	// Test conversation and turn creation
	turnID := g.createConversationAndTurn("test-req-1", chatReq, state)
	if turnID == 0 {
		t.Fatal("Expected non-zero turn ID")
	}

	// Verify conversation was created
	conversationID := agentConversationID("test-req-1", chatReq, state)
	conv, err := db.GetConversation(conversationID)
	if err != nil {
		t.Fatalf("Failed to get conversation: %v", err)
	}

	if conv.ID != conversationID {
		t.Errorf("Expected conversation ID %s, got %s", conversationID, conv.ID)
	}

	// Verify turn was created
	turn, err := db.GetTurn(turnID)
	if err != nil {
		t.Fatalf("Failed to get turn: %v", err)
	}

	if turn.Status != "running" {
		t.Errorf("Expected status 'running', got '%s'", turn.Status)
	}

	// Test duplicate request with different turn_seq (should succeed)
	state2 := &agentSessionState{
		Messages: []chatMessage{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi"},
			{Role: "user", Content: "How are you?"},
		},
	}
	chatReq2 := chatReq
	chatReq2.Messages = state2.Messages

	turnID2 := g.createConversationAndTurn("test-req-1", chatReq2, state2)
	if turnID2 == 0 {
		t.Fatal("Expected non-zero turn ID for second turn")
	}

	if turnID2 == turnID {
		t.Error("Expected different turn IDs for different turns")
	}
}

// TestGatewaySaveTokenDetails tests token details saving with turn ID
func TestGatewaySaveTokenDetails(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{
		db: db,
	}

	// Create conversation and turn first
	chatReq := unifiedChatRequest{
		RequestID:     "test-req-2",
		WorkspaceRoot: "/test",
		ModelName:     "test-model",
		AgentMode:     cursorAgentModeAgent,
		Messages:      []chatMessage{{Role: "user", Content: "Test"}},
	}

	state := &agentSessionState{
		Messages: []chatMessage{{Role: "user", Content: "Test"}},
	}

	turnID := g.createConversationAndTurn("test-req-2", chatReq, state)
	if turnID == 0 {
		t.Fatal("Expected non-zero turn ID")
	}

	conversationID := agentConversationID("test-req-2", chatReq, state)

	// Save token details
	g.saveTokenDetails(conversationID, turnID, 100, 50, 20, 10)

	// Verify token details can be retrieved by turn ID
	details, err := db.GetTokenDetailsByTurn(turnID)
	if err != nil {
		t.Fatalf("Failed to get token details by turn: %v", err)
	}

	if len(details) == 0 {
		t.Fatal("Expected token details, got none")
	}

	if details[0].TurnID != turnID {
		t.Errorf("Expected turn ID %d, got %d", turnID, details[0].TurnID)
	}

	if details[0].PromptTokens != 100 {
		t.Errorf("Expected prompt tokens 100, got %d", details[0].PromptTokens)
	}
}

// TestGatewayTurnStatusLifecycle tests turn status updates through Gateway
func TestGatewayTurnStatusLifecycle(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{
		db: db,
	}

	chatReq := unifiedChatRequest{
		RequestID:     "test-req-3",
		WorkspaceRoot: "/test",
		ModelName:     "test-model",
		AgentMode:     cursorAgentModeAgent,
		Messages:      []chatMessage{{Role: "user", Content: "Test"}},
	}

	state := &agentSessionState{
		Messages: []chatMessage{{Role: "user", Content: "Test"}},
	}

	// Create turn
	turnID := g.createConversationAndTurn("test-req-3", chatReq, state)
	if turnID == 0 {
		t.Fatal("Expected non-zero turn ID")
	}

	// Verify initial status
	turn, err := db.GetTurn(turnID)
	if err != nil {
		t.Fatalf("Failed to get turn: %v", err)
	}

	if turn.Status != "running" {
		t.Errorf("Expected status 'running', got '%s'", turn.Status)
	}

	// Update to completed
	if err := db.UpdateTurnStatus(turnID, "completed", ""); err != nil {
		t.Fatalf("Failed to update turn status: %v", err)
	}

	// Verify completed status
	turn, err = db.GetTurn(turnID)
	if err != nil {
		t.Fatalf("Failed to get turn: %v", err)
	}

	if turn.Status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", turn.Status)
	}

	if turn.CompletedAt == nil {
		t.Error("Expected CompletedAt to be set")
	}
}

// TestGatewayToolCallPersistence tests tool call creation and update
func TestGatewayToolCallPersistence(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{
		db: db,
	}

	chatReq := unifiedChatRequest{
		RequestID:     "test-req-4",
		WorkspaceRoot: "/test",
		ModelName:     "test-model",
		AgentMode:     cursorAgentModeAgent,
		Messages:      []chatMessage{{Role: "user", Content: "Test"}},
	}

	state := &agentSessionState{
		Messages: []chatMessage{{Role: "user", Content: "Test"}},
	}

	// Create turn
	turnID := g.createConversationAndTurn("test-req-4", chatReq, state)
	if turnID == 0 {
		t.Fatal("Expected non-zero turn ID")
	}

	conversationID := agentConversationID("test-req-4", chatReq, state)

	// Create tool call
	toolCall := &database.ToolCall{
		TurnID:         turnID,
		ConversationID: conversationID,
		ToolCallID:     "call-123",
		ToolName:       "Read",
		ToolArgs:       `{"file_path": "/test/file.go"}`,
		Status:         "running",
	}

	if err := db.CreateToolCall(toolCall); err != nil {
		t.Fatalf("Failed to create tool call: %v", err)
	}

	if toolCall.ID == 0 {
		t.Fatal("Expected non-zero tool call ID")
	}

	// Update tool call result
	resultText := "package main\n\nfunc main() {}"
	if err := db.UpdateToolCallResult("call-123", resultText, "completed", ""); err != nil {
		t.Fatalf("Failed to update tool call result: %v", err)
	}

	// Verify tool call was updated
	toolCalls, err := db.GetToolCallsByTurn(turnID)
	if err != nil {
		t.Fatalf("Failed to get tool calls: %v", err)
	}

	if len(toolCalls) == 0 {
		t.Fatal("Expected tool calls, got none")
	}

	if toolCalls[0].Status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", toolCalls[0].Status)
	}

	if toolCalls[0].ToolResult != resultText {
		t.Errorf("Expected result '%s', got '%s'", resultText, toolCalls[0].ToolResult)
	}
}

// TestIsToolResultError tests the tool result error detection function
func TestIsToolResultError(t *testing.T) {
	tests := []struct {
		name     string
		result   string
		expected bool
	}{
		{"Empty result", "", false},
		{"Success result", "File read successfully", false},
		{"Error keyword", "Error: file not found", true},
		{"Failed keyword", "Operation failed", true},
		{"Failure keyword", "Failure to connect", true},
		{"Mixed case error", "ERROR: something went wrong", true},
		{"Normal text with error word", "This is an error-free result", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isToolResultError(tt.result)
			if result != tt.expected {
				t.Errorf("isToolResultError(%q) = %v, expected %v", tt.result, result, tt.expected)
			}
		})
	}
}
