package database

// SQL schema for persistent storage of conversations, messages, and agent state

const schemaSQL = `
-- Conversations table: stores conversation metadata
CREATE TABLE IF NOT EXISTS conversations (
    id TEXT PRIMARY KEY,
    workspace_root TEXT NOT NULL,
    model_name TEXT NOT NULL,
    agent_mode TEXT NOT NULL DEFAULT 'agent',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    parent_id TEXT,
    fork_point_turn INTEGER,
    is_active INTEGER NOT NULL DEFAULT 1,
    metadata TEXT -- JSON blob for additional metadata
);

CREATE INDEX IF NOT EXISTS idx_conversations_workspace ON conversations(workspace_root);
CREATE INDEX IF NOT EXISTS idx_conversations_updated ON conversations(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversations_parent ON conversations(parent_id);

-- Turns table: stores each user-assistant interaction turn
CREATE TABLE IF NOT EXISTS turns (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    turn_seq INTEGER NOT NULL,
    request_id TEXT NOT NULL,
    model_name TEXT NOT NULL,
    thinking_effort TEXT,
    status TEXT NOT NULL DEFAULT 'pending', -- pending, running, completed, failed
    started_at INTEGER NOT NULL,
    completed_at INTEGER,
    error_message TEXT,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
    UNIQUE(conversation_id, turn_seq)
);

CREATE INDEX IF NOT EXISTS idx_turns_conversation ON turns(conversation_id, turn_seq);
CREATE INDEX IF NOT EXISTS idx_turns_request ON turns(request_id);

-- Messages table: stores individual messages (user, assistant, tool_call, tool_result)
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    turn_id INTEGER NOT NULL,
    conversation_id TEXT NOT NULL,
    message_seq INTEGER NOT NULL,
    role TEXT NOT NULL, -- user, assistant, tool
    content TEXT, -- message content or tool result
    tool_name TEXT, -- for tool_call and tool_result
    tool_call_id TEXT, -- for tool_call and tool_result
    tool_args TEXT, -- JSON for tool arguments
    created_at INTEGER NOT NULL,
    blob_id TEXT, -- reference to blobs table for large content
    FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_messages_turn ON messages(turn_id, message_seq);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, created_at);
CREATE INDEX IF NOT EXISTS idx_messages_tool_call ON messages(tool_call_id);

-- Blobs table: stores large content (prompts, responses, context)
CREATE TABLE IF NOT EXISTS blobs (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    blob_type TEXT NOT NULL, -- prompt, response, context, tool_result, file_content
    data BLOB NOT NULL,
    size_bytes INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER, -- for cleanup
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_blobs_conversation ON blobs(conversation_id);
CREATE INDEX IF NOT EXISTS idx_blobs_expires ON blobs(expires_at);

-- Checkpoints table: stores conversation state snapshots
CREATE TABLE IF NOT EXISTS checkpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    turn_id INTEGER NOT NULL,
    checkpoint_type TEXT NOT NULL, -- bubble, conversation, intermediate
    token_details TEXT NOT NULL, -- JSON with used/max/prompt/completion/cache tokens
    context_snapshot TEXT, -- JSON with visible files, selected context
    created_at INTEGER NOT NULL,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
    FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_conversation ON checkpoints(conversation_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_checkpoints_turn ON checkpoints(turn_id);

-- Token details table: stores detailed token usage per turn
CREATE TABLE IF NOT EXISTS token_details (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    turn_id INTEGER NOT NULL,
    conversation_id TEXT NOT NULL,
    provider_call_seq INTEGER NOT NULL DEFAULT 1, -- for multi-pass agent calls
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    is_estimated INTEGER NOT NULL DEFAULT 0, -- 1 if estimated, 0 if from provider
    created_at INTEGER NOT NULL,
    FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_token_details_turn ON token_details(turn_id, provider_call_seq);
CREATE INDEX IF NOT EXISTS idx_token_details_conversation ON token_details(conversation_id, created_at);

-- Tool calls table: stores tool invocations and results
CREATE TABLE IF NOT EXISTS tool_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    turn_id INTEGER NOT NULL,
    conversation_id TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_args TEXT NOT NULL, -- JSON
    tool_result TEXT, -- JSON or text result
    status TEXT NOT NULL DEFAULT 'pending', -- pending, running, completed, failed
    started_at INTEGER NOT NULL,
    completed_at INTEGER,
    duration_ms INTEGER,
    error_message TEXT,
    FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_turn ON tool_calls(turn_id, started_at);
CREATE INDEX IF NOT EXISTS idx_tool_calls_id ON tool_calls(tool_call_id);

-- Context snapshots table: stores file sync and context state
CREATE TABLE IF NOT EXISTS context_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    turn_id INTEGER,
    snapshot_type TEXT NOT NULL, -- file_sync, tab_context, selected_context
    file_path TEXT,
    content TEXT,
    version INTEGER,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
    FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_context_snapshots_conversation ON context_snapshots(conversation_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_context_snapshots_file ON context_snapshots(file_path, version DESC);

-- Usage events table: stores aggregated statistics
CREATE TABLE IF NOT EXISTS usage_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL, -- conversation_start, turn_complete, tool_call, error
    conversation_id TEXT,
    turn_id INTEGER,
    model_name TEXT,
    prompt_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    cache_read_tokens INTEGER DEFAULT 0,
    cache_write_tokens INTEGER DEFAULT 0,
    cost_estimate REAL DEFAULT 0.0,
    success INTEGER NOT NULL DEFAULT 1,
    error_message TEXT,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE SET NULL,
    FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_usage_events_created ON usage_events(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_events_type ON usage_events(event_type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_events_model ON usage_events(model_name, created_at DESC);
`
