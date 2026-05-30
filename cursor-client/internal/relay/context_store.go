package relay

import (
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ContextStore provides unified access to file content from multiple sources
// Priority: Cursor exec/control > FileSync cache > TabContext > Disk fallback
type ContextStore struct {
	mu sync.RWMutex

	// Current workspace
	workspaceRoot string

	// File content sources (in priority order)
	fileSyncCache   map[string]*FileSyncEntry
	tabContextCache map[string]*TabContextEntry
	selectedContext *SelectedContextSnapshot

	// Current file tracking
	currentFile   string
	visibleFiles  []string
	recentFiles   []string
	selectedFiles []string

	// Metadata
	lastUpdated time.Time
}

// FileSyncEntry represents a file synced from Cursor
type FileSyncEntry struct {
	Path         string
	Contents     string
	Version      int
	Language     string
	IsUnsaved    bool
	LastModified time.Time
}

// TabContextEntry represents tab context from Cursor
type TabContextEntry struct {
	Path         string
	Content      string
	CursorLine   int
	CursorColumn int
	LastAccessed time.Time
}

// SelectedContextSnapshot represents user-selected context
type SelectedContextSnapshot struct {
	Files      []string
	CodeBlocks []CodeBlock
	UpdatedAt  time.Time
}

// CodeBlock represents a selected code snippet
type CodeBlock struct {
	FilePath  string
	StartLine int
	EndLine   int
	Content   string
}

// NewContextStore creates a new context store
func NewContextStore(workspaceRoot string) *ContextStore {
	return &ContextStore{
		workspaceRoot:   workspaceRoot,
		fileSyncCache:   make(map[string]*FileSyncEntry),
		tabContextCache: make(map[string]*TabContextEntry),
		lastUpdated:     time.Now(),
	}
}

// SetWorkspaceRoot updates the workspace root
func (cs *ContextStore) SetWorkspaceRoot(root string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.workspaceRoot = root
	cs.lastUpdated = time.Now()
}

// GetWorkspaceRoot returns the current workspace root
func (cs *ContextStore) GetWorkspaceRoot() string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.workspaceRoot
}

// UpdateFileSync updates file content from FileSync
func (cs *ContextStore) UpdateFileSync(path string, contents string, version int, language string, isUnsaved bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	normalizedPath := cs.normalizePath(path)
	cs.fileSyncCache[normalizedPath] = &FileSyncEntry{
		Path:         normalizedPath,
		Contents:     contents,
		Version:      version,
		Language:     language,
		IsUnsaved:    isUnsaved,
		LastModified: time.Now(),
	}
	cs.lastUpdated = time.Now()

	log.Printf("[ContextStore] FileSync updated: %s (version=%d, unsaved=%v, bytes=%d)",
		normalizedPath, version, isUnsaved, len(contents))
}

// UpdateTabContext updates tab context from Cursor
func (cs *ContextStore) UpdateTabContext(path string, content string, cursorLine int, cursorColumn int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	normalizedPath := cs.normalizePath(path)
	cs.tabContextCache[normalizedPath] = &TabContextEntry{
		Path:         normalizedPath,
		Content:      content,
		CursorLine:   cursorLine,
		CursorColumn: cursorColumn,
		LastAccessed: time.Now(),
	}
	cs.lastUpdated = time.Now()
}

// UpdateSelectedContext updates user-selected context
func (cs *ContextStore) UpdateSelectedContext(files []string, codeBlocks []CodeBlock) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	normalizedFiles := make([]string, len(files))
	for i, f := range files {
		normalizedFiles[i] = cs.normalizePath(f)
	}

	cs.selectedContext = &SelectedContextSnapshot{
		Files:      normalizedFiles,
		CodeBlocks: codeBlocks,
		UpdatedAt:  time.Now(),
	}
	cs.lastUpdated = time.Now()
}

// SetCurrentFile sets the currently active file
func (cs *ContextStore) SetCurrentFile(path string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.currentFile = cs.normalizePath(path)
	cs.lastUpdated = time.Now()
}

// GetCurrentFile returns the currently active file
func (cs *ContextStore) GetCurrentFile() string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.currentFile
}

// SetVisibleFiles sets the list of visible files in editor
func (cs *ContextStore) SetVisibleFiles(files []string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.visibleFiles = make([]string, len(files))
	for i, f := range files {
		cs.visibleFiles[i] = cs.normalizePath(f)
	}
	cs.lastUpdated = time.Now()
}

// GetVisibleFiles returns the list of visible files
func (cs *ContextStore) GetVisibleFiles() []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	result := make([]string, len(cs.visibleFiles))
	copy(result, cs.visibleFiles)
	return result
}

// SetRecentFiles sets the list of recently viewed files
func (cs *ContextStore) SetRecentFiles(files []string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.recentFiles = make([]string, len(files))
	for i, f := range files {
		cs.recentFiles[i] = cs.normalizePath(f)
	}
	cs.lastUpdated = time.Now()
}

// GetRecentFiles returns the list of recently viewed files
func (cs *ContextStore) GetRecentFiles() []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	result := make([]string, len(cs.recentFiles))
	copy(result, cs.recentFiles)
	return result
}

// GetFileContent retrieves file content with priority fallback
// Priority: FileSync (unsaved) > FileSync (saved) > TabContext > nil (caller should read from disk)
func (cs *ContextStore) GetFileContent(path string) (content string, source string, found bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	normalizedPath := cs.normalizePath(path)

	// Priority 1: FileSync cache (especially unsaved content)
	if entry, ok := cs.fileSyncCache[normalizedPath]; ok {
		if entry.IsUnsaved {
			return entry.Contents, "filesync_unsaved", true
		}
		// Even saved FileSync is more recent than disk
		return entry.Contents, "filesync", true
	}

	// Priority 2: TabContext cache
	if entry, ok := cs.tabContextCache[normalizedPath]; ok {
		return entry.Content, "tabcontext", true
	}

	// Priority 3: Selected context (if this file is in selected context)
	if cs.selectedContext != nil {
		for _, block := range cs.selectedContext.CodeBlocks {
			if cs.normalizePath(block.FilePath) == normalizedPath {
				return block.Content, "selected_context", true
			}
		}
	}

	// Not found in any cache - caller should read from disk or Cursor exec
	return "", "", false
}

// GetSelectedContext returns the current selected context snapshot
func (cs *ContextStore) GetSelectedContext() *SelectedContextSnapshot {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if cs.selectedContext == nil {
		return nil
	}

	// Return a copy to prevent external modification
	snapshot := &SelectedContextSnapshot{
		Files:      make([]string, len(cs.selectedContext.Files)),
		CodeBlocks: make([]CodeBlock, len(cs.selectedContext.CodeBlocks)),
		UpdatedAt:  cs.selectedContext.UpdatedAt,
	}
	copy(snapshot.Files, cs.selectedContext.Files)
	copy(snapshot.CodeBlocks, cs.selectedContext.CodeBlocks)

	return snapshot
}

// IsFileOpen checks if a file is currently open in the editor
func (cs *ContextStore) IsFileOpen(path string) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	normalizedPath := cs.normalizePath(path)

	// Check visible files
	for _, f := range cs.visibleFiles {
		if f == normalizedPath {
			return true
		}
	}

	// Check if it's the current file
	if cs.currentFile == normalizedPath {
		return true
	}

	return false
}

// GetContextSummary returns a summary of current context for debugging
func (cs *ContextStore) GetContextSummary() map[string]interface{} {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	summary := map[string]interface{}{
		"workspace_root":      cs.workspaceRoot,
		"current_file":        cs.currentFile,
		"visible_files":       len(cs.visibleFiles),
		"recent_files":        len(cs.recentFiles),
		"filesync_entries":    len(cs.fileSyncCache),
		"tabcontext_entries":  len(cs.tabContextCache),
		"has_selected_context": cs.selectedContext != nil,
		"last_updated":        cs.lastUpdated,
	}

	if cs.selectedContext != nil {
		summary["selected_files"] = len(cs.selectedContext.Files)
		summary["selected_blocks"] = len(cs.selectedContext.CodeBlocks)
	}

	return summary
}

// ClearStaleEntries removes entries older than the specified duration
func (cs *ContextStore) ClearStaleEntries(maxAge time.Duration) int {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	removed := 0

	// Clear stale FileSync entries
	for path, entry := range cs.fileSyncCache {
		if entry.LastModified.Before(cutoff) {
			delete(cs.fileSyncCache, path)
			removed++
		}
	}

	// Clear stale TabContext entries
	for path, entry := range cs.tabContextCache {
		if entry.LastAccessed.Before(cutoff) {
			delete(cs.tabContextCache, path)
			removed++
		}
	}

	if removed > 0 {
		log.Printf("[ContextStore] Cleared %d stale entries", removed)
	}

	return removed
}

// normalizePath normalizes a file path for consistent comparison
func (cs *ContextStore) normalizePath(path string) string {
	if path == "" {
		return ""
	}

	// Remove leading/trailing whitespace
	path = strings.TrimSpace(path)

	// Convert to forward slashes for consistent comparison
	normalized := filepath.ToSlash(path)

	// Check if it's an absolute path (Unix-style or Windows-style)
	// Unix: starts with /
	// Windows: starts with C:/ or similar
	isAbsolute := strings.HasPrefix(normalized, "/") ||
		(len(normalized) >= 3 && normalized[1] == ':' && (normalized[2] == '/' || normalized[2] == '\\'))

	// If it's an absolute path, return as-is
	if isAbsolute {
		return normalized
	}

	// Path is relative - if we have a workspace root, join them
	if cs.workspaceRoot != "" {
		joined := filepath.Join(cs.workspaceRoot, path)
		return filepath.ToSlash(joined)
	}

	// No workspace root, return normalized relative path
	return normalized
}

// ResolveRelativePath resolves a relative path against the workspace root
func (cs *ContextStore) ResolveRelativePath(relativePath string) string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	// Clean the path first
	relativePath = strings.TrimSpace(relativePath)
	normalized := filepath.ToSlash(relativePath)

	// Check if it's already absolute (Unix-style or Windows-style)
	isAbsolute := strings.HasPrefix(normalized, "/") ||
		(len(normalized) >= 3 && normalized[1] == ':' && (normalized[2] == '/' || normalized[2] == '\\'))

	// If already absolute, return as-is
	if isAbsolute {
		return normalized
	}

	// If no workspace root, return the relative path as-is
	if cs.workspaceRoot == "" {
		return normalized
	}

	// Join with workspace root
	joined := filepath.Join(cs.workspaceRoot, relativePath)
	return filepath.ToSlash(joined)
}
