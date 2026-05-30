package database

import (
	"database/sql"
	"fmt"
	"time"
)

// CreateConversation creates a new conversation record
func (db *DB) CreateConversation(conv *Conversation) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if conv.CreatedAt.IsZero() {
		conv.CreatedAt = time.Now()
	}
	if conv.UpdatedAt.IsZero() {
		conv.UpdatedAt = conv.CreatedAt
	}

	query := `
		INSERT INTO conversations (id, workspace_root, model_name, agent_mode, created_at, updated_at, parent_id, fork_point_turn, is_active, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := db.conn.Exec(query,
		conv.ID,
		conv.WorkspaceRoot,
		conv.ModelName,
		conv.AgentMode,
		timeToUnix(conv.CreatedAt),
		timeToUnix(conv.UpdatedAt),
		conv.ParentID,
		conv.ForkPointTurn,
		conv.IsActive,
		conv.Metadata,
	)
	return err
}

// GetConversation retrieves a conversation by ID
func (db *DB) GetConversation(id string) (*Conversation, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, workspace_root, model_name, agent_mode, created_at, updated_at, parent_id, fork_point_turn, is_active, metadata
		FROM conversations
		WHERE id = ?
	`
	var conv Conversation
	var createdAt, updatedAt int64
	var parentID sql.NullString
	var forkPointTurn sql.NullInt64
	var isActive int
	var metadata sql.NullString

	err := db.conn.QueryRow(query, id).Scan(
		&conv.ID,
		&conv.WorkspaceRoot,
		&conv.ModelName,
		&conv.AgentMode,
		&createdAt,
		&updatedAt,
		&parentID,
		&forkPointTurn,
		&isActive,
		&metadata,
	)
	if err != nil {
		return nil, err
	}

	conv.CreatedAt = unixToTime(createdAt)
	conv.UpdatedAt = unixToTime(updatedAt)
	if parentID.Valid {
		conv.ParentID = &parentID.String
	}
	if forkPointTurn.Valid {
		turn := int(forkPointTurn.Int64)
		conv.ForkPointTurn = &turn
	}
	conv.IsActive = isActive == 1
	if metadata.Valid {
		conv.Metadata = metadata.String
	}

	return &conv, nil
}

// UpdateConversation updates a conversation's metadata
func (db *DB) UpdateConversation(conv *Conversation) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	conv.UpdatedAt = time.Now()

	query := `
		UPDATE conversations
		SET workspace_root = ?, model_name = ?, agent_mode = ?, updated_at = ?, parent_id = ?, fork_point_turn = ?, is_active = ?, metadata = ?
		WHERE id = ?
	`
	_, err := db.conn.Exec(query,
		conv.WorkspaceRoot,
		conv.ModelName,
		conv.AgentMode,
		timeToUnix(conv.UpdatedAt),
		conv.ParentID,
		conv.ForkPointTurn,
		conv.IsActive,
		conv.Metadata,
		conv.ID,
	)
	return err
}

// CreateTurn creates a new turn record
func (db *DB) CreateTurn(turn *Turn) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if turn.StartedAt.IsZero() {
		turn.StartedAt = time.Now()
	}

	query := `
		INSERT INTO turns (conversation_id, turn_seq, request_id, model_name, thinking_effort, status, started_at, completed_at, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := db.conn.Exec(query,
		turn.ConversationID,
		turn.TurnSeq,
		turn.RequestID,
		turn.ModelName,
		turn.ThinkingEffort,
		turn.Status,
		timeToUnix(turn.StartedAt),
		nullTimeToUnix(turn.CompletedAt),
		turn.ErrorMessage,
	)
	if err != nil {
		return err
	}

	turn.ID, err = result.LastInsertId()
	return err
}

// GetTurn retrieves a turn by ID
func (db *DB) GetTurn(id int64) (*Turn, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, conversation_id, turn_seq, request_id, model_name, thinking_effort, status, started_at, completed_at, error_message
		FROM turns
		WHERE id = ?
	`
	var turn Turn
	var startedAt int64
	var completedAt sql.NullInt64
	var thinkingEffort, errorMessage sql.NullString

	err := db.conn.QueryRow(query, id).Scan(
		&turn.ID,
		&turn.ConversationID,
		&turn.TurnSeq,
		&turn.RequestID,
		&turn.ModelName,
		&thinkingEffort,
		&turn.Status,
		&startedAt,
		&completedAt,
		&errorMessage,
	)
	if err != nil {
		return nil, err
	}

	turn.StartedAt = unixToTime(startedAt)
	if completedAt.Valid {
		t := unixToTime(completedAt.Int64)
		turn.CompletedAt = &t
	}
	if thinkingEffort.Valid {
		turn.ThinkingEffort = thinkingEffort.String
	}
	if errorMessage.Valid {
		turn.ErrorMessage = errorMessage.String
	}

	return &turn, nil
}

// UpdateTurnStatus updates a turn's status and completion time
func (db *DB) UpdateTurnStatus(turnID int64, status string, errorMessage string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	completedAt := time.Now()
	query := `
		UPDATE turns
		SET status = ?, completed_at = ?, error_message = ?
		WHERE id = ?
	`
	_, err := db.conn.Exec(query, status, timeToUnix(completedAt), errorMessage, turnID)
	return err
}

// GetTurnsByConversation retrieves all turns for a conversation
func (db *DB) GetTurnsByConversation(conversationID string) ([]*Turn, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, conversation_id, turn_seq, request_id, model_name, thinking_effort, status, started_at, completed_at, error_message
		FROM turns
		WHERE conversation_id = ?
		ORDER BY turn_seq ASC
	`
	rows, err := db.conn.Query(query, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []*Turn
	for rows.Next() {
		var turn Turn
		var startedAt int64
		var completedAt sql.NullInt64
		var thinkingEffort, errorMessage sql.NullString

		err := rows.Scan(
			&turn.ID,
			&turn.ConversationID,
			&turn.TurnSeq,
			&turn.RequestID,
			&turn.ModelName,
			&thinkingEffort,
			&turn.Status,
			&startedAt,
			&completedAt,
			&errorMessage,
		)
		if err != nil {
			return nil, err
		}

		turn.StartedAt = unixToTime(startedAt)
		if completedAt.Valid {
			t := unixToTime(completedAt.Int64)
			turn.CompletedAt = &t
		}
		if thinkingEffort.Valid {
			turn.ThinkingEffort = thinkingEffort.String
		}
		if errorMessage.Valid {
			turn.ErrorMessage = errorMessage.String
		}

		turns = append(turns, &turn)
	}

	return turns, rows.Err()
}

// CreateMessage creates a new message record
func (db *DB) CreateMessage(msg *Message) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}

	query := `
		INSERT INTO messages (turn_id, conversation_id, message_seq, role, content, tool_name, tool_call_id, tool_args, created_at, blob_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := db.conn.Exec(query,
		msg.TurnID,
		msg.ConversationID,
		msg.MessageSeq,
		msg.Role,
		msg.Content,
		msg.ToolName,
		msg.ToolCallID,
		msg.ToolArgs,
		timeToUnix(msg.CreatedAt),
		msg.BlobID,
	)
	if err != nil {
		return err
	}

	msg.ID, err = result.LastInsertId()
	return err
}

// GetMessagesByTurn retrieves all messages for a turn
func (db *DB) GetMessagesByTurn(turnID int64) ([]*Message, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, turn_id, conversation_id, message_seq, role, content, tool_name, tool_call_id, tool_args, created_at, blob_id
		FROM messages
		WHERE turn_id = ?
		ORDER BY message_seq ASC
	`
	rows, err := db.conn.Query(query, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		var msg Message
		var createdAt int64
		var toolName, toolCallID, toolArgs, blobID sql.NullString

		err := rows.Scan(
			&msg.ID,
			&msg.TurnID,
			&msg.ConversationID,
			&msg.MessageSeq,
			&msg.Role,
			&msg.Content,
			&toolName,
			&toolCallID,
			&toolArgs,
			&createdAt,
			&blobID,
		)
		if err != nil {
			return nil, err
		}

		msg.CreatedAt = unixToTime(createdAt)
		if toolName.Valid {
			msg.ToolName = toolName.String
		}
		if toolCallID.Valid {
			msg.ToolCallID = toolCallID.String
		}
		if toolArgs.Valid {
			msg.ToolArgs = toolArgs.String
		}
		if blobID.Valid {
			msg.BlobID = blobID.String
		}

		messages = append(messages, &msg)
	}

	return messages, rows.Err()
}

// CreateTokenDetails creates a new token details record
func (db *DB) CreateTokenDetails(td *TokenDetails) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if td.CreatedAt.IsZero() {
		td.CreatedAt = time.Now()
	}

	query := `
		INSERT INTO token_details (turn_id, conversation_id, provider_call_seq, prompt_tokens, completion_tokens, total_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, is_estimated, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := db.conn.Exec(query,
		td.TurnID,
		td.ConversationID,
		td.ProviderCallSeq,
		td.PromptTokens,
		td.CompletionTokens,
		td.TotalTokens,
		td.CacheReadTokens,
		td.CacheWriteTokens,
		td.ReasoningTokens,
		td.IsEstimated,
		timeToUnix(td.CreatedAt),
	)
	if err != nil {
		return err
	}

	td.ID, err = result.LastInsertId()
	return err
}

// GetTokenDetailsByTurn retrieves all token details for a turn
func (db *DB) GetTokenDetailsByTurn(turnID int64) ([]*TokenDetails, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, turn_id, conversation_id, provider_call_seq, prompt_tokens, completion_tokens, total_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, is_estimated, created_at
		FROM token_details
		WHERE turn_id = ?
		ORDER BY provider_call_seq ASC
	`
	rows, err := db.conn.Query(query, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var details []*TokenDetails
	for rows.Next() {
		var td TokenDetails
		var createdAt int64
		var isEstimated int

		err := rows.Scan(
			&td.ID,
			&td.TurnID,
			&td.ConversationID,
			&td.ProviderCallSeq,
			&td.PromptTokens,
			&td.CompletionTokens,
			&td.TotalTokens,
			&td.CacheReadTokens,
			&td.CacheWriteTokens,
			&td.ReasoningTokens,
			&isEstimated,
			&createdAt,
		)
		if err != nil {
			return nil, err
		}

		td.CreatedAt = unixToTime(createdAt)
		td.IsEstimated = isEstimated == 1

		details = append(details, &td)
	}

	return details, rows.Err()
}

// GetAggregatedTokensByConversation returns cumulative token usage for a conversation
func (db *DB) GetAggregatedTokensByConversation(conversationID string) (*TokenDetails, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
			COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
			COALESCE(SUM(reasoning_tokens), 0) as reasoning_tokens
		FROM token_details
		WHERE conversation_id = ?
	`
	var td TokenDetails
	td.ConversationID = conversationID

	err := db.conn.QueryRow(query, conversationID).Scan(
		&td.PromptTokens,
		&td.CompletionTokens,
		&td.TotalTokens,
		&td.CacheReadTokens,
		&td.CacheWriteTokens,
		&td.ReasoningTokens,
	)
	if err != nil {
		return nil, err
	}

	return &td, nil
}

// CreateBlob creates a new blob record
func (db *DB) CreateBlob(blob *Blob) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if blob.CreatedAt.IsZero() {
		blob.CreatedAt = time.Now()
	}

	query := `
		INSERT INTO blobs (id, conversation_id, blob_type, data, size_bytes, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := db.conn.Exec(query,
		blob.ID,
		blob.ConversationID,
		blob.BlobType,
		blob.Data,
		blob.SizeBytes,
		timeToUnix(blob.CreatedAt),
		nullTimeToUnix(blob.ExpiresAt),
	)
	return err
}

// GetBlob retrieves a blob by ID
func (db *DB) GetBlob(id string) (*Blob, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, conversation_id, blob_type, data, size_bytes, created_at, expires_at
		FROM blobs
		WHERE id = ?
	`
	var blob Blob
	var createdAt int64
	var expiresAt sql.NullInt64

	err := db.conn.QueryRow(query, id).Scan(
		&blob.ID,
		&blob.ConversationID,
		&blob.BlobType,
		&blob.Data,
		&blob.SizeBytes,
		&createdAt,
		&expiresAt,
	)
	if err != nil {
		return nil, err
	}

	blob.CreatedAt = unixToTime(createdAt)
	if expiresAt.Valid {
		t := unixToTime(expiresAt.Int64)
		blob.ExpiresAt = &t
	}

	return &blob, nil
}

// CreateCheckpoint creates a new checkpoint record
func (db *DB) CreateCheckpoint(cp *Checkpoint) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}

	query := `
		INSERT INTO checkpoints (conversation_id, turn_id, checkpoint_type, token_details, context_snapshot, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	result, err := db.conn.Exec(query,
		cp.ConversationID,
		cp.TurnID,
		cp.CheckpointType,
		cp.TokenDetails,
		cp.ContextSnapshot,
		timeToUnix(cp.CreatedAt),
	)
	if err != nil {
		return err
	}

	cp.ID, err = result.LastInsertId()
	return err
}

// GetCheckpointsByConversation retrieves checkpoints for a conversation
func (db *DB) GetCheckpointsByConversation(conversationID string, limit int) ([]*Checkpoint, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, conversation_id, turn_id, checkpoint_type, token_details, context_snapshot, created_at
		FROM checkpoints
		WHERE conversation_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`
	rows, err := db.conn.Query(query, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var checkpoints []*Checkpoint
	for rows.Next() {
		var cp Checkpoint
		var createdAt int64
		var contextSnapshot sql.NullString

		err := rows.Scan(
			&cp.ID,
			&cp.ConversationID,
			&cp.TurnID,
			&cp.CheckpointType,
			&cp.TokenDetails,
			&contextSnapshot,
			&createdAt,
		)
		if err != nil {
			return nil, err
		}

		cp.CreatedAt = unixToTime(createdAt)
		if contextSnapshot.Valid {
			cp.ContextSnapshot = contextSnapshot.String
		}

		checkpoints = append(checkpoints, &cp)
	}

	return checkpoints, rows.Err()
}

// CreateUsageEvent creates a new usage event record
func (db *DB) CreateUsageEvent(event *UsageEvent) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}

	query := `
		INSERT INTO usage_events (event_type, conversation_id, turn_id, model_name, prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cost_estimate, success, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := db.conn.Exec(query,
		event.EventType,
		event.ConversationID,
		event.TurnID,
		event.ModelName,
		event.PromptTokens,
		event.CompletionTokens,
		event.CacheReadTokens,
		event.CacheWriteTokens,
		event.CostEstimate,
		event.Success,
		event.ErrorMessage,
		timeToUnix(event.CreatedAt),
	)
	if err != nil {
		return err
	}

	event.ID, err = result.LastInsertId()
	return err
}

// GetUsageStatistics retrieves aggregated usage statistics
func (db *DB) GetUsageStatistics(since time.Time) (map[string]interface{}, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT
			COUNT(*) as total_events,
			SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END) as failure_count,
			COALESCE(SUM(prompt_tokens), 0) as total_prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as total_completion_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(cache_write_tokens), 0) as total_cache_write_tokens,
			COALESCE(SUM(cost_estimate), 0.0) as total_cost
		FROM usage_events
		WHERE created_at >= ?
	`
	var totalEvents, successCount, failureCount int64
	var promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int64
	var totalCost float64

	err := db.conn.QueryRow(query, timeToUnix(since)).Scan(
		&totalEvents,
		&successCount,
		&failureCount,
		&promptTokens,
		&completionTokens,
		&cacheReadTokens,
		&cacheWriteTokens,
		&totalCost,
	)
	if err != nil {
		return nil, err
	}

	stats := map[string]interface{}{
		"total_events":           totalEvents,
		"success_count":          successCount,
		"failure_count":          failureCount,
		"total_prompt_tokens":    promptTokens,
		"total_completion_tokens": completionTokens,
		"total_cache_read_tokens": cacheReadTokens,
		"total_cache_write_tokens": cacheWriteTokens,
		"total_cost":             totalCost,
	}

	// Calculate cache hit rate
	if promptTokens > 0 {
		cacheHitRate := float64(cacheReadTokens) / float64(promptTokens+cacheReadTokens) * 100
		stats["cache_hit_rate"] = fmt.Sprintf("%.2f%%", cacheHitRate)
	} else {
		stats["cache_hit_rate"] = "0.00%"
	}

	return stats, nil
}
