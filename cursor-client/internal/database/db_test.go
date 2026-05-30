package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDatabaseOpen(t *testing.T) {
	// Create temp directory for test database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Test opening database
	db, err := Open(Config{Path: dbPath})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Verify database file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("Database file was not created")
	}

	// Verify WAL files exist (WAL mode)
	walPath := dbPath + "-wal"
	if _, err := os.Stat(walPath); os.IsNotExist(err) {
		t.Logf("WAL file not created yet (may be created on first write)")
	}
}

func TestConversationCRUD(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Test Create
	conv := &Conversation{
		ID:            "test-conv-123",
		WorkspaceRoot: "/test/workspace",
		ModelName:     "gpt-4",
		AgentMode:     "agent",
		IsActive:      true,
		Metadata:      `{"key":"value"}`,
	}

	err := db.CreateConversation(conv)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Test Get
	retrieved, err := db.GetConversation("test-conv-123")
	if err != nil {
		t.Fatalf("Failed to get conversation: %v", err)
	}

	if retrieved.ID != conv.ID {
		t.Errorf("Expected ID %s, got %s", conv.ID, retrieved.ID)
	}
	if retrieved.WorkspaceRoot != conv.WorkspaceRoot {
		t.Errorf("Expected workspace %s, got %s", conv.WorkspaceRoot, retrieved.WorkspaceRoot)
	}
	if retrieved.ModelName != conv.ModelName {
		t.Errorf("Expected model %s, got %s", conv.ModelName, retrieved.ModelName)
	}
	if !retrieved.IsActive {
		t.Errorf("Expected IsActive true, got false")
	}

	// Test Update
	retrieved.ModelName = "gpt-4-turbo"
	retrieved.IsActive = false
	err = db.UpdateConversation(retrieved)
	if err != nil {
		t.Fatalf("Failed to update conversation: %v", err)
	}

	updated, err := db.GetConversation("test-conv-123")
	if err != nil {
		t.Fatalf("Failed to get updated conversation: %v", err)
	}
	if updated.ModelName != "gpt-4-turbo" {
		t.Errorf("Expected updated model gpt-4-turbo, got %s", updated.ModelName)
	}
	if updated.IsActive {
		t.Errorf("Expected IsActive false, got true")
	}
}

func TestTurnCRUD(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create conversation first
	conv := &Conversation{
		ID:            "test-conv-456",
		WorkspaceRoot: "/test/workspace",
		ModelName:     "gpt-4",
		AgentMode:     "agent",
		IsActive:      true,
	}
	db.CreateConversation(conv)

	// Test Create Turn
	turn := &Turn{
		ConversationID: "test-conv-456",
		TurnSeq:        1,
		RequestID:      "req-123",
		ModelName:      "gpt-4",
		ThinkingEffort: "medium",
		Status:         "running",
	}

	err := db.CreateTurn(turn)
	if err != nil {
		t.Fatalf("Failed to create turn: %v", err)
	}

	if turn.ID == 0 {
		t.Errorf("Expected turn ID to be set, got 0")
	}

	// Test Get Turn
	retrieved, err := db.GetTurn(turn.ID)
	if err != nil {
		t.Fatalf("Failed to get turn: %v", err)
	}

	if retrieved.ConversationID != turn.ConversationID {
		t.Errorf("Expected conversation ID %s, got %s", turn.ConversationID, retrieved.ConversationID)
	}
	if retrieved.TurnSeq != turn.TurnSeq {
		t.Errorf("Expected turn seq %d, got %d", turn.TurnSeq, retrieved.TurnSeq)
	}
	if retrieved.Status != "running" {
		t.Errorf("Expected status running, got %s", retrieved.Status)
	}

	// Test Update Turn Status
	err = db.UpdateTurnStatus(turn.ID, "completed", "")
	if err != nil {
		t.Fatalf("Failed to update turn status: %v", err)
	}

	updated, err := db.GetTurn(turn.ID)
	if err != nil {
		t.Fatalf("Failed to get updated turn: %v", err)
	}
	if updated.Status != "completed" {
		t.Errorf("Expected status completed, got %s", updated.Status)
	}
	if updated.CompletedAt == nil {
		t.Errorf("Expected CompletedAt to be set")
	}

	// Test Get Turns By Conversation
	turns, err := db.GetTurnsByConversation("test-conv-456")
	if err != nil {
		t.Fatalf("Failed to get turns by conversation: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("Expected 1 turn, got %d", len(turns))
	}
}

func TestMessageCRUD(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Setup conversation and turn
	conv := &Conversation{
		ID:            "test-conv-789",
		WorkspaceRoot: "/test/workspace",
		ModelName:     "gpt-4",
		AgentMode:     "agent",
		IsActive:      true,
	}
	db.CreateConversation(conv)

	turn := &Turn{
		ConversationID: "test-conv-789",
		TurnSeq:        1,
		RequestID:      "req-456",
		ModelName:      "gpt-4",
		Status:         "running",
	}
	db.CreateTurn(turn)

	// Test Create Message
	msg := &Message{
		TurnID:         turn.ID,
		ConversationID: "test-conv-789",
		MessageSeq:     1,
		Role:           "user",
		Content:        "Hello, world!",
	}

	err := db.CreateMessage(msg)
	if err != nil {
		t.Fatalf("Failed to create message: %v", err)
	}

	if msg.ID == 0 {
		t.Errorf("Expected message ID to be set, got 0")
	}

	// Test Get Messages By Turn
	messages, err := db.GetMessagesByTurn(turn.ID)
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Role != "user" {
		t.Errorf("Expected role user, got %s", messages[0].Role)
	}
	if messages[0].Content != "Hello, world!" {
		t.Errorf("Expected content 'Hello, world!', got %s", messages[0].Content)
	}
}

func TestTokenDetailsCRUD(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Setup conversation and turn
	conv := &Conversation{
		ID:            "test-conv-token",
		WorkspaceRoot: "/test/workspace",
		ModelName:     "gpt-4",
		AgentMode:     "agent",
		IsActive:      true,
	}
	db.CreateConversation(conv)

	turn := &Turn{
		ConversationID: "test-conv-token",
		TurnSeq:        1,
		RequestID:      "req-token",
		ModelName:      "gpt-4",
		Status:         "running",
	}
	db.CreateTurn(turn)

	// Test Create Token Details
	td := &TokenDetails{
		TurnID:           turn.ID,
		ConversationID:   "test-conv-token",
		ProviderCallSeq:  1,
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		CacheReadTokens:  200,
		CacheWriteTokens: 100,
		ReasoningTokens:  50,
		IsEstimated:      false,
	}

	err := db.CreateTokenDetails(td)
	if err != nil {
		t.Fatalf("Failed to create token details: %v", err)
	}

	// Test Get Token Details By Turn
	details, err := db.GetTokenDetailsByTurn(turn.ID)
	if err != nil {
		t.Fatalf("Failed to get token details: %v", err)
	}

	if len(details) != 1 {
		t.Fatalf("Expected 1 token detail, got %d", len(details))
	}

	if details[0].PromptTokens != 1000 {
		t.Errorf("Expected prompt tokens 1000, got %d", details[0].PromptTokens)
	}
	if details[0].CacheReadTokens != 200 {
		t.Errorf("Expected cache read tokens 200, got %d", details[0].CacheReadTokens)
	}

	// Test multiple provider calls
	td2 := &TokenDetails{
		TurnID:           turn.ID,
		ConversationID:   "test-conv-token",
		ProviderCallSeq:  2,
		PromptTokens:     800,
		CompletionTokens: 400,
		TotalTokens:      1200,
		CacheReadTokens:  300,
		CacheWriteTokens: 50,
		IsEstimated:      false,
	}
	db.CreateTokenDetails(td2)

	details, err = db.GetTokenDetailsByTurn(turn.ID)
	if err != nil {
		t.Fatalf("Failed to get token details: %v", err)
	}

	if len(details) != 2 {
		t.Errorf("Expected 2 token details, got %d", len(details))
	}
}

func TestAggregatedTokens(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Setup conversation and turns
	conv := &Conversation{
		ID:            "test-conv-agg",
		WorkspaceRoot: "/test/workspace",
		ModelName:     "gpt-4",
		AgentMode:     "agent",
		IsActive:      true,
	}
	db.CreateConversation(conv)

	// Create multiple turns with token details
	for i := 1; i <= 3; i++ {
		turn := &Turn{
			ConversationID: "test-conv-agg",
			TurnSeq:        i,
			RequestID:      "req-agg",
			ModelName:      "gpt-4",
			Status:         "completed",
		}
		db.CreateTurn(turn)

		td := &TokenDetails{
			TurnID:           turn.ID,
			ConversationID:   "test-conv-agg",
			ProviderCallSeq:  1,
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
			CacheReadTokens:  200,
			CacheWriteTokens: 100,
		}
		db.CreateTokenDetails(td)
	}

	// Test Aggregated Tokens
	aggregated, err := db.GetAggregatedTokensByConversation("test-conv-agg")
	if err != nil {
		t.Fatalf("Failed to get aggregated tokens: %v", err)
	}

	expectedPrompt := 3000
	expectedCompletion := 1500
	expectedTotal := 4500
	expectedCacheRead := 600

	if aggregated.PromptTokens != expectedPrompt {
		t.Errorf("Expected prompt tokens %d, got %d", expectedPrompt, aggregated.PromptTokens)
	}
	if aggregated.CompletionTokens != expectedCompletion {
		t.Errorf("Expected completion tokens %d, got %d", expectedCompletion, aggregated.CompletionTokens)
	}
	if aggregated.TotalTokens != expectedTotal {
		t.Errorf("Expected total tokens %d, got %d", expectedTotal, aggregated.TotalTokens)
	}
	if aggregated.CacheReadTokens != expectedCacheRead {
		t.Errorf("Expected cache read tokens %d, got %d", expectedCacheRead, aggregated.CacheReadTokens)
	}
}

func TestBlobCRUD(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Setup conversation
	conv := &Conversation{
		ID:            "test-conv-blob",
		WorkspaceRoot: "/test/workspace",
		ModelName:     "gpt-4",
		AgentMode:     "agent",
		IsActive:      true,
	}
	db.CreateConversation(conv)

	// Test Create Blob
	data := []byte("This is test blob data")
	blob := &Blob{
		ID:             "blob-123",
		ConversationID: "test-conv-blob",
		BlobType:       "prompt",
		Data:           data,
		SizeBytes:      len(data),
	}

	err := db.CreateBlob(blob)
	if err != nil {
		t.Fatalf("Failed to create blob: %v", err)
	}

	// Test Get Blob
	retrieved, err := db.GetBlob("blob-123")
	if err != nil {
		t.Fatalf("Failed to get blob: %v", err)
	}

	if retrieved.BlobType != "prompt" {
		t.Errorf("Expected blob type prompt, got %s", retrieved.BlobType)
	}
	if string(retrieved.Data) != string(data) {
		t.Errorf("Expected data %s, got %s", string(data), string(retrieved.Data))
	}
	if retrieved.SizeBytes != len(data) {
		t.Errorf("Expected size %d, got %d", len(data), retrieved.SizeBytes)
	}
}

func TestCheckpointCRUD(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Setup conversation and turn
	conv := &Conversation{
		ID:            "test-conv-cp",
		WorkspaceRoot: "/test/workspace",
		ModelName:     "gpt-4",
		AgentMode:     "agent",
		IsActive:      true,
	}
	db.CreateConversation(conv)

	turn := &Turn{
		ConversationID: "test-conv-cp",
		TurnSeq:        1,
		RequestID:      "req-cp",
		ModelName:      "gpt-4",
		Status:         "completed",
	}
	db.CreateTurn(turn)

	// Test Create Checkpoint
	cp := &Checkpoint{
		ConversationID:  "test-conv-cp",
		TurnID:          turn.ID,
		CheckpointType:  "bubble",
		TokenDetails:    `{"used":1500,"max":10000}`,
		ContextSnapshot: `{"files":["main.go"]}`,
	}

	err := db.CreateCheckpoint(cp)
	if err != nil {
		t.Fatalf("Failed to create checkpoint: %v", err)
	}

	// Test Get Checkpoints
	checkpoints, err := db.GetCheckpointsByConversation("test-conv-cp", 10)
	if err != nil {
		t.Fatalf("Failed to get checkpoints: %v", err)
	}

	if len(checkpoints) != 1 {
		t.Fatalf("Expected 1 checkpoint, got %d", len(checkpoints))
	}

	if checkpoints[0].CheckpointType != "bubble" {
		t.Errorf("Expected checkpoint type bubble, got %s", checkpoints[0].CheckpointType)
	}
}

func TestUsageStatistics(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create usage events
	for i := 0; i < 5; i++ {
		event := &UsageEvent{
			EventType:        "turn_complete",
			ModelName:        "gpt-4",
			PromptTokens:     1000,
			CompletionTokens: 500,
			CacheReadTokens:  200,
			CacheWriteTokens: 100,
			CostEstimate:     0.05,
			Success:          true,
		}
		db.CreateUsageEvent(event)
	}

	// Create one failed event
	failedEvent := &UsageEvent{
		EventType:    "turn_complete",
		ModelName:    "gpt-4",
		Success:      false,
		ErrorMessage: "timeout",
	}
	db.CreateUsageEvent(failedEvent)

	// Test Get Usage Statistics
	since := time.Now().Add(-1 * time.Hour)
	stats, err := db.GetUsageStatistics(since)
	if err != nil {
		t.Fatalf("Failed to get usage statistics: %v", err)
	}

	totalEvents := stats["total_events"].(int64)
	successCount := stats["success_count"].(int64)
	failureCount := stats["failure_count"].(int64)

	if totalEvents != 6 {
		t.Errorf("Expected 6 total events, got %d", totalEvents)
	}
	if successCount != 5 {
		t.Errorf("Expected 5 successful events, got %d", successCount)
	}
	if failureCount != 1 {
		t.Errorf("Expected 1 failed event, got %d", failureCount)
	}

	// Check token totals
	promptTokens := stats["total_prompt_tokens"].(int64)
	if promptTokens != 5000 {
		t.Errorf("Expected 5000 prompt tokens, got %d", promptTokens)
	}
}

func TestCleanupExpiredBlobs(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Setup conversation
	conv := &Conversation{
		ID:            "test-conv-cleanup",
		WorkspaceRoot: "/test/workspace",
		ModelName:     "gpt-4",
		AgentMode:     "agent",
		IsActive:      true,
	}
	db.CreateConversation(conv)

	// Create expired blob
	expiredTime := time.Now().Add(-1 * time.Hour)
	expiredBlob := &Blob{
		ID:             "blob-expired",
		ConversationID: "test-conv-cleanup",
		BlobType:       "temp",
		Data:           []byte("expired data"),
		SizeBytes:      12,
		ExpiresAt:      &expiredTime,
	}
	db.CreateBlob(expiredBlob)

	// Create non-expired blob
	futureTime := time.Now().Add(1 * time.Hour)
	activeBlob := &Blob{
		ID:             "blob-active",
		ConversationID: "test-conv-cleanup",
		BlobType:       "temp",
		Data:           []byte("active data"),
		SizeBytes:      11,
		ExpiresAt:      &futureTime,
	}
	db.CreateBlob(activeBlob)

	// Test Cleanup
	removed, err := db.CleanupExpiredBlobs()
	if err != nil {
		t.Fatalf("Failed to cleanup expired blobs: %v", err)
	}

	if removed != 1 {
		t.Errorf("Expected 1 blob removed, got %d", removed)
	}

	// Verify expired blob is gone
	_, err = db.GetBlob("blob-expired")
	if err == nil {
		t.Errorf("Expected expired blob to be deleted")
	}

	// Verify active blob still exists
	_, err = db.GetBlob("blob-active")
	if err != nil {
		t.Errorf("Expected active blob to still exist: %v", err)
	}
}

func TestToolCallCRUD(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Setup conversation and turn
	conv := &Conversation{
		ID:            "test-conv-toolcall",
		WorkspaceRoot: "/test/workspace",
		ModelName:     "gpt-4",
		AgentMode:     "agent",
		IsActive:      true,
	}
	db.CreateConversation(conv)

	turn := &Turn{
		ConversationID: "test-conv-toolcall",
		TurnSeq:        1,
		RequestID:      "req-toolcall",
		ModelName:      "gpt-4",
		Status:         "running",
	}
	db.CreateTurn(turn)

	// Test CreateToolCall
	toolCall := &ToolCall{
		TurnID:         turn.ID,
		ConversationID: "test-conv-toolcall",
		ToolCallID:     "call-123",
		ToolName:       "Read",
		ToolArgs:       `{"file_path": "/test/file.go"}`,
		Status:         "pending",
	}

	err := db.CreateToolCall(toolCall)
	if err != nil {
		t.Fatalf("Failed to create tool call: %v", err)
	}

	if toolCall.ID == 0 {
		t.Errorf("Expected tool call ID to be set, got 0")
	}

	// Test GetToolCallsByTurn
	toolCalls, err := db.GetToolCallsByTurn(turn.ID)
	if err != nil {
		t.Fatalf("Failed to get tool calls: %v", err)
	}

	if len(toolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(toolCalls))
	}

	if toolCalls[0].ToolName != "Read" {
		t.Errorf("Expected tool name Read, got %s", toolCalls[0].ToolName)
	}
	if toolCalls[0].Status != "pending" {
		t.Errorf("Expected status pending, got %s", toolCalls[0].Status)
	}

	// Test UpdateToolCallResult
	result := `{"content": "file contents here"}`
	err = db.UpdateToolCallResult(toolCall.ToolCallID, result, "completed", "")
	if err != nil {
		t.Fatalf("Failed to update tool call result: %v", err)
	}

	// Verify update
	updatedCalls, err := db.GetToolCallsByTurn(turn.ID)
	if err != nil {
		t.Fatalf("Failed to get updated tool calls: %v", err)
	}

	if updatedCalls[0].Status != "completed" {
		t.Errorf("Expected status completed, got %s", updatedCalls[0].Status)
	}
	if updatedCalls[0].ToolResult != result {
		t.Errorf("Expected result %s, got %s", result, updatedCalls[0].ToolResult)
	}
	if updatedCalls[0].CompletedAt == nil {
		t.Errorf("Expected CompletedAt to be set")
	}

	// Test error case
	errorCall := &ToolCall{
		TurnID:         turn.ID,
		ConversationID: "test-conv-toolcall",
		ToolCallID:     "call-456",
		ToolName:       "Write",
		ToolArgs:       `{"file_path": "/test/output.go"}`,
		Status:         "pending",
	}
	db.CreateToolCall(errorCall)

	err = db.UpdateToolCallResult(errorCall.ToolCallID, "", "error", "Permission denied")
	if err != nil {
		t.Fatalf("Failed to update tool call with error: %v", err)
	}

	errorCalls, err := db.GetToolCallsByTurn(turn.ID)
	if err != nil {
		t.Fatalf("Failed to get tool calls: %v", err)
	}

	// Find the error call
	var foundError bool
	for _, tc := range errorCalls {
		if tc.ToolCallID == "call-456" {
			foundError = true
			if tc.Status != "error" {
				t.Errorf("Expected status error, got %s", tc.Status)
			}
			if tc.ErrorMessage != "Permission denied" {
				t.Errorf("Expected error message 'Permission denied', got %s", tc.ErrorMessage)
			}
		}
	}

	if !foundError {
		t.Errorf("Expected to find error tool call")
	}
}

// Helper function to setup test database
func setupTestDB(t *testing.T) *DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(Config{Path: dbPath})
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}

	return db
}
