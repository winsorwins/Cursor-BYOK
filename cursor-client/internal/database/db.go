package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database connection with thread-safe operations
type DB struct {
	conn *sql.DB
	mu   sync.RWMutex
	path string
}

// Config holds database configuration
type Config struct {
	Path string // Path to SQLite database file
}

// Open opens or creates a SQLite database
func Open(cfg Config) (*DB, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("database path is required")
	}

	// Ensure directory exists
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database with appropriate flags
	conn, err := sql.Open("sqlite", cfg.Path+"?_journal_mode=WAL&_timeout=5000&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool settings
	conn.SetMaxOpenConns(1) // SQLite works best with single writer
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(0)

	// Test connection
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db := &DB{
		conn: conn,
		path: cfg.Path,
	}

	// Initialize schema
	if err := db.initSchema(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	log.Printf("[Database] Opened database at %s", cfg.Path)
	return db, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	if db.conn != nil {
		return db.conn.Close()
	}
	return nil
}

// initSchema creates all tables if they don't exist
func (db *DB) initSchema() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(schemaSQL)
	if err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	log.Printf("[Database] Schema initialized successfully")
	return nil
}

// Conversation represents a conversation record
type Conversation struct {
	ID            string
	WorkspaceRoot string
	ModelName     string
	AgentMode     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ParentID      *string
	ForkPointTurn *int
	IsActive      bool
	Metadata      string
}

// Turn represents a turn record
type Turn struct {
	ID             int64
	ConversationID string
	TurnSeq        int
	RequestID      string
	ModelName      string
	ThinkingEffort string
	Status         string
	StartedAt      time.Time
	CompletedAt    *time.Time
	ErrorMessage   string
}

// Message represents a message record
type Message struct {
	ID             int64
	TurnID         int64
	ConversationID string
	MessageSeq     int
	Role           string
	Content        string
	ToolName       string
	ToolCallID     string
	ToolArgs       string
	CreatedAt      time.Time
	BlobID         string
}

// TokenDetails represents token usage for a turn
type TokenDetails struct {
	ID                int64
	TurnID            int64
	ConversationID    string
	ProviderCallSeq   int
	PromptTokens      int
	CompletionTokens  int
	TotalTokens       int
	CacheReadTokens   int
	CacheWriteTokens  int
	ReasoningTokens   int
	IsEstimated       bool
	CreatedAt         time.Time
}

// ToolCall represents a tool invocation
type ToolCall struct {
	ID             int64
	TurnID         int64
	ConversationID string
	ToolCallID     string
	ToolName       string
	ToolArgs       string
	ToolResult     string
	Status         string
	StartedAt      time.Time
	CompletedAt    *time.Time
	DurationMs     *int64
	ErrorMessage   string
}

// Blob represents a large content blob
type Blob struct {
	ID             string
	ConversationID string
	BlobType       string
	Data           []byte
	SizeBytes      int
	CreatedAt      time.Time
	ExpiresAt      *time.Time
}

// Checkpoint represents a conversation state checkpoint
type Checkpoint struct {
	ID              int64
	ConversationID  string
	TurnID          int64
	CheckpointType  string
	TokenDetails    string
	ContextSnapshot string
	CreatedAt       time.Time
}

// UsageEvent represents an aggregated usage statistic
type UsageEvent struct {
	ID               int64
	EventType        string
	ConversationID   *string
	TurnID           *int64
	ModelName        string
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	CostEstimate     float64
	Success          bool
	ErrorMessage     string
	CreatedAt        time.Time
}

// Helper function to convert time to Unix timestamp
func timeToUnix(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().Unix()
	}
	return t.Unix()
}

// Helper function to convert Unix timestamp to time
func unixToTime(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

// Helper function to convert nullable time to Unix timestamp
func nullTimeToUnix(t *time.Time) *int64 {
	if t == nil || t.IsZero() {
		return nil
	}
	unix := t.Unix()
	return &unix
}

// Helper function to convert nullable Unix timestamp to time
func nullUnixToTime(unix *int64) *time.Time {
	if unix == nil || *unix == 0 {
		return nil
	}
	t := time.Unix(*unix, 0)
	return &t
}
