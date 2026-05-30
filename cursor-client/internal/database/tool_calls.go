package database

import (
	"database/sql"
	"time"
)

// CreateToolCall creates a new tool call record
func (db *DB) CreateToolCall(tc *ToolCall) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if tc.StartedAt.IsZero() {
		tc.StartedAt = time.Now()
	}

	query := `
		INSERT INTO tool_calls (turn_id, conversation_id, tool_call_id, tool_name, tool_args, tool_result, status, started_at, completed_at, duration_ms, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := db.conn.Exec(query,
		tc.TurnID,
		tc.ConversationID,
		tc.ToolCallID,
		tc.ToolName,
		tc.ToolArgs,
		tc.ToolResult,
		tc.Status,
		timeToUnix(tc.StartedAt),
		nullTimeToUnix(tc.CompletedAt),
		tc.DurationMs,
		tc.ErrorMessage,
	)
	if err != nil {
		return err
	}

	tc.ID, err = result.LastInsertId()
	return err
}

// UpdateToolCallResult updates a tool call with its result
func (db *DB) UpdateToolCallResult(toolCallID string, result string, status string, errorMessage string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	completedAt := time.Now()
	query := `
		UPDATE tool_calls
		SET tool_result = ?, status = ?, completed_at = ?, error_message = ?
		WHERE tool_call_id = ?
	`
	_, err := db.conn.Exec(query, result, status, timeToUnix(completedAt), errorMessage, toolCallID)
	return err
}

// GetToolCallsByTurn retrieves all tool calls for a turn
func (db *DB) GetToolCallsByTurn(turnID int64) ([]*ToolCall, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, turn_id, conversation_id, tool_call_id, tool_name, tool_args, tool_result, status, started_at, completed_at, duration_ms, error_message
		FROM tool_calls
		WHERE turn_id = ?
		ORDER BY started_at ASC
	`
	rows, err := db.conn.Query(query, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var toolCalls []*ToolCall
	for rows.Next() {
		var tc ToolCall
		var startedAt int64
		var completedAt sql.NullInt64
		var durationMs sql.NullInt64
		var toolResult, errorMessage sql.NullString

		err := rows.Scan(
			&tc.ID,
			&tc.TurnID,
			&tc.ConversationID,
			&tc.ToolCallID,
			&tc.ToolName,
			&tc.ToolArgs,
			&toolResult,
			&tc.Status,
			&startedAt,
			&completedAt,
			&durationMs,
			&errorMessage,
		)
		if err != nil {
			return nil, err
		}

		tc.StartedAt = unixToTime(startedAt)
		if completedAt.Valid {
			t := unixToTime(completedAt.Int64)
			tc.CompletedAt = &t
		}
		if durationMs.Valid {
			duration := durationMs.Int64
			tc.DurationMs = &duration
		}
		if toolResult.Valid {
			tc.ToolResult = toolResult.String
		}
		if errorMessage.Valid {
			tc.ErrorMessage = errorMessage.String
		}

		toolCalls = append(toolCalls, &tc)
	}

	return toolCalls, rows.Err()
}

// ContextSnapshot represents a context snapshot record
type ContextSnapshot struct {
	ID             int64
	ConversationID string
	TurnID         *int64
	SnapshotType   string
	FilePath       string
	Content        string
	Version        int
	CreatedAt      time.Time
}

// CreateContextSnapshot creates a new context snapshot record
func (db *DB) CreateContextSnapshot(cs *ContextSnapshot) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if cs.CreatedAt.IsZero() {
		cs.CreatedAt = time.Now()
	}

	query := `
		INSERT INTO context_snapshots (conversation_id, turn_id, snapshot_type, file_path, content, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	result, err := db.conn.Exec(query,
		cs.ConversationID,
		cs.TurnID,
		cs.SnapshotType,
		cs.FilePath,
		cs.Content,
		cs.Version,
		timeToUnix(cs.CreatedAt),
	)
	if err != nil {
		return err
	}

	cs.ID, err = result.LastInsertId()
	return err
}

// GetLatestContextSnapshot retrieves the latest context snapshot for a file
func (db *DB) GetLatestContextSnapshot(conversationID string, filePath string) (*ContextSnapshot, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, conversation_id, turn_id, snapshot_type, file_path, content, version, created_at
		FROM context_snapshots
		WHERE conversation_id = ? AND file_path = ?
		ORDER BY version DESC, created_at DESC
		LIMIT 1
	`
	var cs ContextSnapshot
	var turnID sql.NullInt64
	var filePathResult sql.NullString
	var createdAt int64

	err := db.conn.QueryRow(query, conversationID, filePath).Scan(
		&cs.ID,
		&cs.ConversationID,
		&turnID,
		&cs.SnapshotType,
		&filePathResult,
		&cs.Content,
		&cs.Version,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}

	if turnID.Valid {
		tid := turnID.Int64
		cs.TurnID = &tid
	}
	if filePathResult.Valid {
		cs.FilePath = filePathResult.String
	}
	cs.CreatedAt = unixToTime(createdAt)

	return &cs, nil
}

// CleanupExpiredBlobs removes expired blobs
func (db *DB) CleanupExpiredBlobs() (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	now := time.Now().Unix()
	query := `DELETE FROM blobs WHERE expires_at IS NOT NULL AND expires_at < ?`
	result, err := db.conn.Exec(query, now)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// CleanupOldConversations removes inactive conversations older than the specified duration
func (db *DB) CleanupOldConversations(olderThan time.Duration) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	cutoff := time.Now().Add(-olderThan).Unix()
	query := `DELETE FROM conversations WHERE is_active = 0 AND updated_at < ?`
	result, err := db.conn.Exec(query, cutoff)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// GetRecentConversations retrieves recent conversations
func (db *DB) GetRecentConversations(limit int) ([]*Conversation, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, workspace_root, model_name, agent_mode, created_at, updated_at, parent_id, fork_point_turn, is_active, metadata
		FROM conversations
		WHERE is_active = 1
		ORDER BY updated_at DESC
		LIMIT ?
	`
	rows, err := db.conn.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conversations []*Conversation
	for rows.Next() {
		var conv Conversation
		var createdAt, updatedAt int64
		var parentID sql.NullString
		var forkPointTurn sql.NullInt64
		var isActive int
		var metadata sql.NullString

		err := rows.Scan(
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

		conversations = append(conversations, &conv)
	}

	return conversations, rows.Err()
}

// GetTurnByRequestID retrieves a turn by request ID
func (db *DB) GetTurnByRequestID(requestID string) (*Turn, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	query := `
		SELECT id, conversation_id, turn_seq, request_id, model_name, thinking_effort, status, started_at, completed_at, error_message
		FROM turns
		WHERE request_id = ?
		LIMIT 1
	`
	var turn Turn
	var startedAt int64
	var completedAt sql.NullInt64
	var thinkingEffort, errorMessage sql.NullString

	err := db.conn.QueryRow(query, requestID).Scan(
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

// GetOrCreateConversation gets an existing conversation or creates a new one
func (db *DB) GetOrCreateConversation(id string, workspaceRoot string, modelName string, agentMode string) (*Conversation, error) {
	// Try to get existing conversation
	conv, err := db.GetConversation(id)
	if err == nil {
		return conv, nil
	}

	// If not found, create new conversation
	if err == sql.ErrNoRows {
		conv = &Conversation{
			ID:            id,
			WorkspaceRoot: workspaceRoot,
			ModelName:     modelName,
			AgentMode:     agentMode,
			IsActive:      true,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
		if err := db.CreateConversation(conv); err != nil {
			return nil, err
		}
		return conv, nil
	}

	return nil, err
}
