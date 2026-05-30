package relay

import (
	"encoding/hex"
	"strings"
	"testing"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
	"cursor-client/internal/database"
	"google.golang.org/protobuf/proto"
)

// TestGatewayStableConversationID tests that same Cursor conversation ID with different requestIDs
// maps to the same conversation and turn_seq increments correctly
func TestGatewayStableConversationID(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{db: db}

	// Simulate ConversationStateStructure with root prompt messages
	rootPrompts := [][]byte{
		[]byte(`{"role":"system","content":"You are a helpful assistant"}`),
		[]byte(`{"role":"user","content":"Initial context"}`),
	}
	conversation := &agentv1.ConversationStateStructure{
		RootPromptMessagesJson: rootPrompts,
	}

	// Extract stable conversation ID
	cursorConvID := extractConversationIDFromState(conversation, "/test/project", cursorAgentModeAgent)
	if cursorConvID == "" {
		t.Fatal("Expected non-empty cursor conversation ID")
	}

	// First request with requestID "req-1"
	chatReq1 := unifiedChatRequest{
		RequestID:            "req-1",
		CursorConversationID: cursorConvID,
		WorkspaceRoot:        "/test/project",
		ModelName:            "claude-opus",
		AgentMode:            cursorAgentModeAgent,
		Messages:             []chatMessage{{Role: "user", Content: "Hello"}},
	}

	state1 := &agentSessionState{
		CursorConversationID: cursorConvID,
		Messages:             chatReq1.Messages,
	}

	turnID1 := g.createConversationAndTurn("req-1", chatReq1, state1)
	if turnID1 == 0 {
		t.Fatal("Expected non-zero turn ID for first request")
	}

	// Second request with different requestID "req-2" but same cursor conversation ID
	chatReq2 := unifiedChatRequest{
		RequestID:            "req-2",
		CursorConversationID: cursorConvID,
		WorkspaceRoot:        "/test/project",
		ModelName:            "claude-opus",
		AgentMode:            cursorAgentModeAgent,
		Messages:             []chatMessage{{Role: "user", Content: "How are you?"}},
	}

	state2 := &agentSessionState{
		CursorConversationID: cursorConvID,
		Messages:             chatReq2.Messages,
	}

	turnID2 := g.createConversationAndTurn("req-2", chatReq2, state2)
	if turnID2 == 0 {
		t.Fatal("Expected non-zero turn ID for second request")
	}

	// Verify both turns belong to the same conversation
	turn1, err := db.GetTurn(turnID1)
	if err != nil {
		t.Fatalf("Failed to get turn 1: %v", err)
	}

	turn2, err := db.GetTurn(turnID2)
	if err != nil {
		t.Fatalf("Failed to get turn 2: %v", err)
	}

	if turn1.ConversationID != turn2.ConversationID {
		t.Errorf("Expected same conversation ID, got turn1=%s turn2=%s", turn1.ConversationID, turn2.ConversationID)
	}

	if turn1.ConversationID != cursorConvID {
		t.Errorf("Expected conversation ID to match cursor conversation ID, got %s want %s", turn1.ConversationID, cursorConvID)
	}

	// Verify turn_seq increments correctly (starting from 1)
	if turn1.TurnSeq != 1 {
		t.Errorf("Expected turn1.TurnSeq=1, got %d", turn1.TurnSeq)
	}

	if turn2.TurnSeq != 2 {
		t.Errorf("Expected turn2.TurnSeq=2, got %d", turn2.TurnSeq)
	}

	// Verify we can query turns by conversation
	turns, err := db.GetTurnsByConversation(cursorConvID)
	if err != nil {
		t.Fatalf("Failed to get turns by conversation: %v", err)
	}

	if len(turns) != 2 {
		t.Errorf("Expected 2 turns, got %d", len(turns))
	}
}

// TestGatewayDifferentConversationsDifferentIDs tests that different cursor conversations
// create different conversation IDs
func TestGatewayDifferentConversationsDifferentIDs(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{db: db}

	// Conversation 1 with root prompts A
	rootPromptsA := [][]byte{
		[]byte(`{"role":"system","content":"You are a helpful assistant"}`),
		[]byte(`{"role":"user","content":"Context A"}`),
	}
	conversationA := &agentv1.ConversationStateStructure{
		RootPromptMessagesJson: rootPromptsA,
	}
	cursorConvID_A := extractConversationIDFromState(conversationA, "/test/project", cursorAgentModeAgent)

	// Conversation 2 with root prompts B (different)
	rootPromptsB := [][]byte{
		[]byte(`{"role":"system","content":"You are a helpful assistant"}`),
		[]byte(`{"role":"user","content":"Context B"}`),
	}
	conversationB := &agentv1.ConversationStateStructure{
		RootPromptMessagesJson: rootPromptsB,
	}
	cursorConvID_B := extractConversationIDFromState(conversationB, "/test/project", cursorAgentModeAgent)

	// Verify different root prompts generate different conversation IDs
	if cursorConvID_A == cursorConvID_B {
		t.Errorf("Expected different conversation IDs for different root prompts, got same: %s", cursorConvID_A)
	}

	// Create turns for both conversations
	chatReq1 := unifiedChatRequest{
		RequestID:            "req-1",
		CursorConversationID: cursorConvID_A,
		WorkspaceRoot:        "/test/project",
		ModelName:            "claude-opus",
		AgentMode:            cursorAgentModeAgent,
		Messages:             []chatMessage{{Role: "user", Content: "Hello A"}},
	}
	state1 := &agentSessionState{
		CursorConversationID: cursorConvID_A,
		Messages:             chatReq1.Messages,
	}
	turnID1 := g.createConversationAndTurn("req-1", chatReq1, state1)

	chatReq2 := unifiedChatRequest{
		RequestID:            "req-2",
		CursorConversationID: cursorConvID_B,
		WorkspaceRoot:        "/test/project",
		ModelName:            "claude-opus",
		AgentMode:            cursorAgentModeAgent,
		Messages:             []chatMessage{{Role: "user", Content: "Hello B"}},
	}
	state2 := &agentSessionState{
		CursorConversationID: cursorConvID_B,
		Messages:             chatReq2.Messages,
	}
	turnID2 := g.createConversationAndTurn("req-2", chatReq2, state2)

	// Verify turns belong to different conversations
	turn1, _ := db.GetTurn(turnID1)
	turn2, _ := db.GetTurn(turnID2)

	if turn1.ConversationID == turn2.ConversationID {
		t.Errorf("Expected different conversation IDs, got same: %s", turn1.ConversationID)
	}
}

// TestGatewayDifferentWorkspacesDifferentConversations tests that different workspaces
// create different conversations
func TestGatewayDifferentWorkspacesDifferentConversations(t *testing.T) {
	// Same root prompts but different workspaces
	rootPrompts := [][]byte{
		[]byte(`{"role":"system","content":"You are a helpful assistant"}`),
	}
	conversation := &agentv1.ConversationStateStructure{
		RootPromptMessagesJson: rootPrompts,
	}

	cursorConvID_WS1 := extractConversationIDFromState(conversation, "/workspace1", cursorAgentModeAgent)
	cursorConvID_WS2 := extractConversationIDFromState(conversation, "/workspace2", cursorAgentModeAgent)

	// Verify different workspaces generate different conversation IDs
	if cursorConvID_WS1 == cursorConvID_WS2 {
		t.Errorf("Expected different conversation IDs for different workspaces, got same: %s", cursorConvID_WS1)
	}
}

// TestGatewayDifferentModesDifferentConversations tests that different agent modes
// create different conversations
func TestGatewayDifferentModesDifferentConversations(t *testing.T) {
	// Same root prompts and workspace but different modes
	rootPrompts := [][]byte{
		[]byte(`{"role":"system","content":"You are a helpful assistant"}`),
	}
	conversation := &agentv1.ConversationStateStructure{
		RootPromptMessagesJson: rootPrompts,
	}

	cursorConvID_Agent := extractConversationIDFromState(conversation, "/test/project", cursorAgentModeAgent)
	cursorConvID_Ask := extractConversationIDFromState(conversation, "/test/project", cursorAgentModeAsk)

	// Verify different modes generate different conversation IDs
	if cursorConvID_Agent == cursorConvID_Ask {
		t.Errorf("Expected different conversation IDs for different modes, got same: %s", cursorConvID_Agent)
	}
}

// TestGatewayFallbackToRequestID tests that when no cursor conversation ID is available,
// the system falls back to requestID
func TestGatewayFallbackToRequestID(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{db: db}

	// Request without cursor conversation ID
	chatReq := unifiedChatRequest{
		RequestID:     "req-fallback",
		WorkspaceRoot: "/test/project",
		ModelName:     "claude-opus",
		AgentMode:     cursorAgentModeAgent,
		Messages:      []chatMessage{{Role: "user", Content: "Hello"}},
	}

	state := &agentSessionState{
		Messages: chatReq.Messages,
	}

	turnID := g.createConversationAndTurn("req-fallback", chatReq, state)
	if turnID == 0 {
		t.Fatal("Expected non-zero turn ID")
	}

	turn, err := db.GetTurn(turnID)
	if err != nil {
		t.Fatalf("Failed to get turn: %v", err)
	}

	// Should fall back to requestID
	if turn.ConversationID != "req-fallback" {
		t.Errorf("Expected conversation ID to be requestID, got %s", turn.ConversationID)
	}
}

// TestGatewayExplicitConversationIDFromProtocol tests that explicit conversation_id
// from AgentRunRequest is read and used with highest priority
func TestGatewayExplicitConversationIDFromProtocol(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{db: db}

	// Simulate AgentRunRequest with explicit conversation_id
	explicitConvID := "cursor-explicit-conv-123"
	runReq := &agentv1.AgentRunRequest{
		ConversationId: &explicitConvID,
	}

	// Create AgentClientMessage with RunRequest
	msg := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: runReq,
		},
	}

	// Marshal to hex for parseAgentClientPayload
	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal AgentClientMessage: %v", err)
	}
	hexData := hex.EncodeToString(data)

	// Parse payload
	state, err := g.parseAgentClientPayload("req-1", hexData)
	if err != nil {
		t.Fatalf("Failed to parse agent client payload: %v", err)
	}

	// Verify CursorConversationID was extracted
	if state.CursorConversationID == "" {
		t.Fatal("Expected non-empty CursorConversationID from explicit conversation_id")
	}

	// Verify it starts with "cursor-conv-" prefix
	if !strings.HasPrefix(state.CursorConversationID, "cursor-conv-") {
		t.Errorf("Expected CursorConversationID to start with 'cursor-conv-', got %s", state.CursorConversationID)
	}

	// Create turn with this conversation ID
	chatReq := unifiedChatRequest{
		RequestID:            "req-1",
		CursorConversationID: state.CursorConversationID,
		WorkspaceRoot:        "/test/project",
		ModelName:            "claude-opus",
		AgentMode:            cursorAgentModeAgent,
		Messages:             []chatMessage{{Role: "user", Content: "Hello"}},
	}

	turnID := g.createConversationAndTurn("req-1", chatReq, state)
	if turnID == 0 {
		t.Fatal("Expected non-zero turn ID")
	}

	turn, err := db.GetTurn(turnID)
	if err != nil {
		t.Fatalf("Failed to get turn: %v", err)
	}

	// Verify conversation ID matches
	if turn.ConversationID != state.CursorConversationID {
		t.Errorf("Expected conversation ID to match state.CursorConversationID, got %s want %s", turn.ConversationID, state.CursorConversationID)
	}
}

// TestGatewayExplicitConversationIDSplitsDifferentConversations tests that different
// explicit conversation_id values create different conversations
func TestGatewayExplicitConversationIDSplitsDifferentConversations(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{db: db}

	// Two different explicit conversation IDs
	explicitConvID1 := "cursor-conv-A"
	explicitConvID2 := "cursor-conv-B"

	// Parse first conversation
	runReq1 := &agentv1.AgentRunRequest{
		ConversationId: &explicitConvID1,
	}
	msg1 := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: runReq1,
		},
	}
	data1, _ := proto.Marshal(msg1)
	hexData1 := hex.EncodeToString(data1)
	state1, _ := g.parseAgentClientPayload("req-1", hexData1)

	// Parse second conversation
	runReq2 := &agentv1.AgentRunRequest{
		ConversationId: &explicitConvID2,
	}
	msg2 := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: runReq2,
		},
	}
	data2, _ := proto.Marshal(msg2)
	hexData2 := hex.EncodeToString(data2)
	state2, _ := g.parseAgentClientPayload("req-2", hexData2)

	// Verify different conversation IDs
	if state1.CursorConversationID == state2.CursorConversationID {
		t.Errorf("Expected different CursorConversationIDs for different explicit conversation_id, got same: %s", state1.CursorConversationID)
	}

	// Create turns
	chatReq1 := unifiedChatRequest{
		RequestID:            "req-1",
		CursorConversationID: state1.CursorConversationID,
		WorkspaceRoot:        "/test/project",
		ModelName:            "claude-opus",
		AgentMode:            cursorAgentModeAgent,
		Messages:             []chatMessage{{Role: "user", Content: "Hello A"}},
	}
	turnID1 := g.createConversationAndTurn("req-1", chatReq1, state1)

	chatReq2 := unifiedChatRequest{
		RequestID:            "req-2",
		CursorConversationID: state2.CursorConversationID,
		WorkspaceRoot:        "/test/project",
		ModelName:            "claude-opus",
		AgentMode:            cursorAgentModeAgent,
		Messages:             []chatMessage{{Role: "user", Content: "Hello B"}},
	}
	turnID2 := g.createConversationAndTurn("req-2", chatReq2, state2)

	// Verify turns belong to different conversations
	turn1, _ := db.GetTurn(turnID1)
	turn2, _ := db.GetTurn(turnID2)

	if turn1.ConversationID == turn2.ConversationID {
		t.Errorf("Expected different conversation IDs, got same: %s", turn1.ConversationID)
	}
}

// TestGatewayTokenDetailsByTurn tests that token details can be queried by turn
func TestGatewayTokenDetailsByTurn(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{db: db}

	// Create conversation and turn
	cursorConvID := "test-conv-123"
	chatReq := unifiedChatRequest{
		RequestID:            "req-1",
		CursorConversationID: cursorConvID,
		WorkspaceRoot:        "/test/project",
		ModelName:            "claude-opus",
		AgentMode:            cursorAgentModeAgent,
		Messages:             []chatMessage{{Role: "user", Content: "Hello"}},
	}

	state := &agentSessionState{
		CursorConversationID: cursorConvID,
		Messages:             chatReq.Messages,
	}

	turnID := g.createConversationAndTurn("req-1", chatReq, state)
	if turnID == 0 {
		t.Fatal("Expected non-zero turn ID")
	}

	// Save token details
	g.saveTokenDetails(cursorConvID, turnID, 1000, 500, 200, 100)

	// Query token details by turn
	details, err := db.GetTokenDetailsByTurn(turnID)
	if err != nil {
		t.Fatalf("Failed to get token details by turn: %v", err)
	}

	if len(details) != 1 {
		t.Fatalf("Expected 1 token detail, got %d", len(details))
	}

	detail := details[0]
	if detail.TurnID != turnID {
		t.Errorf("Expected TurnID=%d, got %d", turnID, detail.TurnID)
	}

	if detail.ConversationID != cursorConvID {
		t.Errorf("Expected ConversationID=%s, got %s", cursorConvID, detail.ConversationID)
	}

	if detail.PromptTokens != 1000 {
		t.Errorf("Expected PromptTokens=1000, got %d", detail.PromptTokens)
	}

	if detail.CompletionTokens != 500 {
		t.Errorf("Expected CompletionTokens=500, got %d", detail.CompletionTokens)
	}

	if detail.CacheReadTokens != 200 {
		t.Errorf("Expected CacheReadTokens=200, got %d", detail.CacheReadTokens)
	}

	if detail.CacheWriteTokens != 100 {
		t.Errorf("Expected CacheWriteTokens=100, got %d", detail.CacheWriteTokens)
	}

	if detail.TotalTokens != 1500 {
		t.Errorf("Expected TotalTokens=1500, got %d", detail.TotalTokens)
	}
}

// TestGatewayExplicitConversationIDFullPath tests the full path:
// AgentRunRequest.ConversationId -> parse -> merge -> agentChatRequest -> createConversationAndTurn
// Ensures that explicit conversation_id survives the entire chain and reaches the database
func TestGatewayExplicitConversationIDFullPath(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{db: db, agentSessions: make(map[string]*agentSessionState)}

	// Simulate AgentRunRequest with explicit conversation_id
	explicitConvID := "my-cursor-conversation-123"
	runReq := &agentv1.AgentRunRequest{
		ConversationId: &explicitConvID,
	}

	msg := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: runReq,
		},
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal AgentClientMessage: %v", err)
	}
	hexData := hex.EncodeToString(data)

	// Step 1: Parse payload
	parsed, err := g.parseAgentClientPayload("req-full-1", hexData)
	if err != nil {
		t.Fatalf("Failed to parse agent client payload: %v", err)
	}

	if parsed.CursorConversationID == "" {
		t.Fatal("Expected non-empty CursorConversationID after parse")
	}

	// Step 2: Merge into session cache (simulates handleLocalBidiAppend)
	g.mergeAgentSession(parsed)

	// Step 3: Read from cache via agentChatRequest (simulates handleLocalAgentRunSSE)
	chatReq, _ := g.agentChatRequest("req-full-1")

	if chatReq.CursorConversationID == "" {
		t.Fatal("Expected non-empty CursorConversationID from agentChatRequest")
	}

	if !strings.HasPrefix(chatReq.CursorConversationID, "cursor-conv-") {
		t.Errorf("Expected CursorConversationID to start with 'cursor-conv-', got %s", chatReq.CursorConversationID)
	}

	// Step 4: Create conversation and turn in DB
	state := g.ensureAgentSession("req-full-1")
	turnID := g.createConversationAndTurn("req-full-1", chatReq, state)
	if turnID == 0 {
		t.Fatal("Expected non-zero turn ID")
	}

	// Verify DB conversation ID is cursor-conv-<hash>, not requestID
	turn, err := db.GetTurn(turnID)
	if err != nil {
		t.Fatalf("Failed to get turn: %v", err)
	}

	if turn.ConversationID == "req-full-1" {
		t.Errorf("Expected conversation ID to be cursor-conv-<hash>, got requestID: %s", turn.ConversationID)
	}

	if !strings.HasPrefix(turn.ConversationID, "cursor-conv-") {
		t.Errorf("Expected conversation ID to start with 'cursor-conv-', got %s", turn.ConversationID)
	}
}

// TestGatewayExplicitConversationIDSameIDDifferentRequestIDs tests that
// same explicit conversation_id with different requestIDs creates turns in the same conversation
func TestGatewayExplicitConversationIDSameIDDifferentRequestIDs(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{db: db, agentSessions: make(map[string]*agentSessionState)}

	explicitConvID := "shared-conversation-456"

	// First request with requestID "req-A"
	runReq1 := &agentv1.AgentRunRequest{
		ConversationId: &explicitConvID,
	}
	msg1 := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: runReq1,
		},
	}
	data1, _ := proto.Marshal(msg1)
	hexData1 := hex.EncodeToString(data1)

	parsed1, _ := g.parseAgentClientPayload("req-A", hexData1)
	g.mergeAgentSession(parsed1)
	chatReq1, _ := g.agentChatRequest("req-A")
	state1 := g.ensureAgentSession("req-A")
	turnID1 := g.createConversationAndTurn("req-A", chatReq1, state1)

	// Second request with requestID "req-B" but same explicit conversation_id
	runReq2 := &agentv1.AgentRunRequest{
		ConversationId: &explicitConvID,
	}
	msg2 := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: runReq2,
		},
	}
	data2, _ := proto.Marshal(msg2)
	hexData2 := hex.EncodeToString(data2)

	parsed2, _ := g.parseAgentClientPayload("req-B", hexData2)
	g.mergeAgentSession(parsed2)
	chatReq2, _ := g.agentChatRequest("req-B")
	state2 := g.ensureAgentSession("req-B")
	turnID2 := g.createConversationAndTurn("req-B", chatReq2, state2)

	// Verify both turns belong to the same conversation
	turn1, _ := db.GetTurn(turnID1)
	turn2, _ := db.GetTurn(turnID2)

	if turn1.ConversationID != turn2.ConversationID {
		t.Errorf("Expected same conversation ID, got turn1=%s turn2=%s", turn1.ConversationID, turn2.ConversationID)
	}

	// Verify turn_seq increments correctly
	if turn1.TurnSeq != 1 {
		t.Errorf("Expected turn1.TurnSeq=1, got %d", turn1.TurnSeq)
	}

	if turn2.TurnSeq != 2 {
		t.Errorf("Expected turn2.TurnSeq=2, got %d", turn2.TurnSeq)
	}
}

// TestGatewayExplicitConversationIDDifferentIDsSplit tests that
// different explicit conversation_ids create different conversations even with same workspace/mode/root
func TestGatewayExplicitConversationIDDifferentIDsSplit(t *testing.T) {
	db, err := database.Open(database.Config{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	g := &Gateway{db: db, agentSessions: make(map[string]*agentSessionState)}

	// Same workspace, mode, and root prompts, but different explicit conversation_ids
	workspace := "/test/project"
	mode := cursorAgentModeAgent

	explicitConvID1 := "conversation-X"
	explicitConvID2 := "conversation-Y"

	// First conversation
	runReq1 := &agentv1.AgentRunRequest{
		ConversationId: &explicitConvID1,
	}
	msg1 := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: runReq1,
		},
	}
	data1, _ := proto.Marshal(msg1)
	hexData1 := hex.EncodeToString(data1)

	parsed1, _ := g.parseAgentClientPayload("req-X", hexData1)
	parsed1.WorkspaceRoot = workspace
	parsed1.AgentMode = mode
	g.mergeAgentSession(parsed1)
	chatReq1, _ := g.agentChatRequest("req-X")
	chatReq1.WorkspaceRoot = workspace
	chatReq1.AgentMode = mode
	state1 := g.ensureAgentSession("req-X")
	turnID1 := g.createConversationAndTurn("req-X", chatReq1, state1)

	// Second conversation with different explicit conversation_id
	runReq2 := &agentv1.AgentRunRequest{
		ConversationId: &explicitConvID2,
	}
	msg2 := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: runReq2,
		},
	}
	data2, _ := proto.Marshal(msg2)
	hexData2 := hex.EncodeToString(data2)

	parsed2, _ := g.parseAgentClientPayload("req-Y", hexData2)
	parsed2.WorkspaceRoot = workspace
	parsed2.AgentMode = mode
	g.mergeAgentSession(parsed2)
	chatReq2, _ := g.agentChatRequest("req-Y")
	chatReq2.WorkspaceRoot = workspace
	chatReq2.AgentMode = mode
	state2 := g.ensureAgentSession("req-Y")
	turnID2 := g.createConversationAndTurn("req-Y", chatReq2, state2)

	// Verify turns belong to different conversations
	turn1, _ := db.GetTurn(turnID1)
	turn2, _ := db.GetTurn(turnID2)

	if turn1.ConversationID == turn2.ConversationID {
		t.Errorf("Expected different conversation IDs, got same: %s", turn1.ConversationID)
	}
}

