package relay

import (
	"path/filepath"
	"testing"
	"time"
)

func TestContextStoreBasic(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	// Test workspace root
	if cs.GetWorkspaceRoot() != "/test/workspace" {
		t.Errorf("Expected workspace root /test/workspace, got %s", cs.GetWorkspaceRoot())
	}

	// Test set workspace root
	cs.SetWorkspaceRoot("/new/workspace")
	if cs.GetWorkspaceRoot() != "/new/workspace" {
		t.Errorf("Expected workspace root /new/workspace, got %s", cs.GetWorkspaceRoot())
	}
}

func TestContextStoreFileSync(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	// Test UpdateFileSync
	cs.UpdateFileSync("/test/workspace/main.go", "package main\n", 1, "go", false)

	// Test GetFileContent - should return from FileSync
	content, source, found := cs.GetFileContent("/test/workspace/main.go")
	if !found {
		t.Fatalf("Expected to find file content")
	}
	if source != "filesync" {
		t.Errorf("Expected source filesync, got %s", source)
	}
	if content != "package main\n" {
		t.Errorf("Expected content 'package main\\n', got %s", content)
	}

	// Test unsaved content priority
	cs.UpdateFileSync("/test/workspace/main.go", "package main\n// unsaved", 2, "go", true)
	content, source, found = cs.GetFileContent("/test/workspace/main.go")
	if !found {
		t.Fatalf("Expected to find file content")
	}
	if source != "filesync_unsaved" {
		t.Errorf("Expected source filesync_unsaved, got %s", source)
	}
	if content != "package main\n// unsaved" {
		t.Errorf("Expected unsaved content, got %s", content)
	}
}

func TestContextStoreTabContext(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	// Test UpdateTabContext
	cs.UpdateTabContext("/test/workspace/utils.go", "package utils\n", 10, 5)

	// Test GetFileContent - should return from TabContext
	content, source, found := cs.GetFileContent("/test/workspace/utils.go")
	if !found {
		t.Fatalf("Expected to find file content")
	}
	if source != "tabcontext" {
		t.Errorf("Expected source tabcontext, got %s", source)
	}
	if content != "package utils\n" {
		t.Errorf("Expected content 'package utils\\n', got %s", content)
	}
}

func TestContextStorePriority(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	path := "/test/workspace/test.go"

	// Add TabContext first
	cs.UpdateTabContext(path, "tab content", 1, 1)
	content, source, _ := cs.GetFileContent(path)
	if source != "tabcontext" {
		t.Errorf("Expected tabcontext, got %s", source)
	}

	// Add FileSync (saved) - should override TabContext
	cs.UpdateFileSync(path, "filesync content", 1, "go", false)
	content, source, _ = cs.GetFileContent(path)
	if source != "filesync" {
		t.Errorf("Expected filesync, got %s", source)
	}
	if content != "filesync content" {
		t.Errorf("Expected filesync content, got %s", content)
	}

	// Add FileSync (unsaved) - should have highest priority
	cs.UpdateFileSync(path, "unsaved content", 2, "go", true)
	content, source, _ = cs.GetFileContent(path)
	if source != "filesync_unsaved" {
		t.Errorf("Expected filesync_unsaved, got %s", source)
	}
	if content != "unsaved content" {
		t.Errorf("Expected unsaved content, got %s", content)
	}
}

func TestContextStoreCurrentFile(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	// Test SetCurrentFile with absolute path
	cs.SetCurrentFile("/test/workspace/main.go")
	if cs.GetCurrentFile() != "/test/workspace/main.go" {
		t.Errorf("Expected current file /test/workspace/main.go, got %s", cs.GetCurrentFile())
	}

	// Test with relative path - should be joined with workspace
	cs.SetCurrentFile("src/utils.go")
	current := cs.GetCurrentFile()
	expected := "/test/workspace/src/utils.go"
	if current != expected {
		t.Errorf("Expected %s, got %s", expected, current)
	}
}

func TestContextStoreVisibleFiles(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	// Use absolute paths
	files := []string{
		"/test/workspace/main.go",
		"/test/workspace/utils.go",
		"/test/workspace/config.go",
	}

	cs.SetVisibleFiles(files)
	visible := cs.GetVisibleFiles()

	if len(visible) != 3 {
		t.Errorf("Expected 3 visible files, got %d", len(visible))
	}

	// Verify files match (they should be normalized)
	for i, f := range visible {
		expected := filepath.ToSlash(files[i])
		if f != expected {
			t.Errorf("File %d: expected %s, got %s", i, expected, f)
		}
	}
}

func TestContextStoreRecentFiles(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	files := []string{
		"/test/workspace/main.go",
		"/test/workspace/utils.go",
	}

	cs.SetRecentFiles(files)
	recent := cs.GetRecentFiles()

	if len(recent) != 2 {
		t.Errorf("Expected 2 recent files, got %d", len(recent))
	}
}

func TestContextStoreSelectedContext(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	files := []string{"/test/workspace/main.go"}
	blocks := []CodeBlock{
		{
			FilePath:  "/test/workspace/main.go",
			StartLine: 10,
			EndLine:   20,
			Content:   "func main() {\n}",
		},
	}

	cs.UpdateSelectedContext(files, blocks)

	snapshot := cs.GetSelectedContext()
	if snapshot == nil {
		t.Fatalf("Expected selected context snapshot")
	}

	if len(snapshot.Files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(snapshot.Files))
	}
	if len(snapshot.CodeBlocks) != 1 {
		t.Errorf("Expected 1 code block, got %d", len(snapshot.CodeBlocks))
	}
	if snapshot.CodeBlocks[0].StartLine != 10 {
		t.Errorf("Expected start line 10, got %d", snapshot.CodeBlocks[0].StartLine)
	}
}

func TestContextStoreIsFileOpen(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	// Set current file
	cs.SetCurrentFile("/test/workspace/main.go")

	// Set visible files
	cs.SetVisibleFiles([]string{
		"/test/workspace/utils.go",
		"/test/workspace/config.go",
	})

	// Test current file
	if !cs.IsFileOpen("/test/workspace/main.go") {
		t.Errorf("Expected main.go to be open")
	}

	// Test visible file
	if !cs.IsFileOpen("/test/workspace/utils.go") {
		t.Errorf("Expected utils.go to be open")
	}

	// Test non-open file
	if cs.IsFileOpen("/test/workspace/other.go") {
		t.Errorf("Expected other.go to not be open")
	}
}

func TestContextStorePathNormalization(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	testCases := []struct {
		input       string
		description string
	}{
		{"/test/workspace/main.go", "absolute path"},
		{"main.go", "relative path"},
		{"src/utils.go", "relative path with directory"},
	}

	for _, tc := range testCases {
		// Add file with input path
		cs.UpdateFileSync(tc.input, "content", 1, "go", false)

		// The file should be retrievable by the same path after normalization
		_, _, found := cs.GetFileContent(tc.input)
		if !found {
			t.Errorf("Path normalization failed for %s (%s): could not retrieve by same path", tc.input, tc.description)
		}
	}

	// Test that absolute and relative paths to the same file are treated as the same
	cs.UpdateFileSync("/test/workspace/test.go", "absolute content", 1, "go", false)
	_, _, _ = cs.GetFileContent("/test/workspace/test.go")

	cs.UpdateFileSync("test.go", "relative content", 2, "go", false)
	content2, _, _ := cs.GetFileContent("test.go")

	// Both should retrieve the same file (the relative one updated it)
	if content2 != "relative content" {
		t.Errorf("Expected relative path to update the same file")
	}

	// Test: absolute path write, then relative path update, then absolute path read
	cs.UpdateFileSync("/test/workspace/cross-test.go", "initial", 1, "go", false)
	cs.UpdateFileSync("cross-test.go", "updated by relative", 2, "go", false)

	// Read back with absolute path - should get the updated content
	contentAbs, _, foundAbs := cs.GetFileContent("/test/workspace/cross-test.go")
	if !foundAbs {
		t.Errorf("Expected to find cross-test.go by absolute path after relative update")
	}
	if contentAbs != "updated by relative" {
		t.Errorf("Expected absolute path read to return updated content, got: %s", contentAbs)
	}
}

func TestContextStoreResolveRelativePath(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	testCases := []struct {
		input    string
		expected string
	}{
		{"main.go", "/test/workspace/main.go"},
		{"src/utils.go", "/test/workspace/src/utils.go"},
		{"/absolute/path.go", "/absolute/path.go"},
		{"C:/absolute/windows.go", "C:/absolute/windows.go"},
	}

	for _, tc := range testCases {
		result := cs.ResolveRelativePath(tc.input)
		if result != tc.expected {
			t.Errorf("ResolveRelativePath(%s): expected %s, got %s", tc.input, tc.expected, result)
		}
	}
}

func TestContextStoreContextSummary(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	// Add some data with absolute paths
	cs.SetCurrentFile("/test/workspace/main.go")
	cs.SetVisibleFiles([]string{"/test/workspace/main.go", "/test/workspace/utils.go"})
	cs.UpdateFileSync("/test/workspace/main.go", "content", 1, "go", false)
	cs.UpdateTabContext("/test/workspace/utils.go", "content", 1, 1)

	summary := cs.GetContextSummary()

	if summary["workspace_root"] != "/test/workspace" {
		t.Errorf("Expected workspace_root /test/workspace, got %v", summary["workspace_root"])
	}
	if summary["current_file"] != "/test/workspace/main.go" {
		t.Errorf("Expected current_file /test/workspace/main.go, got %v", summary["current_file"])
	}
	if summary["visible_files"] != 2 {
		t.Errorf("Expected 2 visible files, got %v", summary["visible_files"])
	}
	if summary["filesync_entries"] != 1 {
		t.Errorf("Expected 1 filesync entry, got %v", summary["filesync_entries"])
	}
	if summary["tabcontext_entries"] != 1 {
		t.Errorf("Expected 1 tabcontext entry, got %v", summary["tabcontext_entries"])
	}
}

func TestContextStoreClearStaleEntries(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	// Add some entries
	cs.UpdateFileSync("/test/workspace/old.go", "old content", 1, "go", false)
	cs.UpdateFileSync("/test/workspace/new.go", "new content", 1, "go", false)
	cs.UpdateTabContext("/test/workspace/old-tab.go", "old tab", 1, 1)

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Add a fresh entry
	cs.UpdateFileSync("/test/workspace/fresh.go", "fresh content", 1, "go", false)

	// Clear entries older than 50ms (should remove old.go, new.go, and old-tab.go, but not fresh.go)
	removed := cs.ClearStaleEntries(50 * time.Millisecond)

	// Verify that some entries were removed
	if removed == 0 {
		t.Errorf("Expected some entries to be removed, got 0")
	}

	// Verify fresh entry still exists
	_, _, found := cs.GetFileContent("/test/workspace/fresh.go")
	if !found {
		t.Errorf("Expected fresh.go to still exist")
	}

	// Verify old entries are gone
	_, _, foundOld := cs.GetFileContent("/test/workspace/old.go")
	if foundOld {
		t.Errorf("Expected old.go to be removed")
	}
}

func TestContextStoreSelectedContextPriority(t *testing.T) {
	cs := NewContextStore("/test/workspace")

	path := "/test/workspace/selected.go"

	// Add selected context
	blocks := []CodeBlock{
		{
			FilePath:  path,
			StartLine: 1,
			EndLine:   10,
			Content:   "selected content",
		},
	}
	cs.UpdateSelectedContext([]string{path}, blocks)

	// Without FileSync or TabContext, should return from selected context
	content, source, found := cs.GetFileContent(path)
	if !found {
		t.Fatalf("Expected to find content from selected context")
	}
	if source != "selected_context" {
		t.Errorf("Expected source selected_context, got %s", source)
	}
	if content != "selected content" {
		t.Errorf("Expected 'selected content', got %s", content)
	}

	// Add TabContext - should override selected context
	cs.UpdateTabContext(path, "tab content", 1, 1)
	content, source, found = cs.GetFileContent(path)
	if source != "tabcontext" {
		t.Errorf("Expected tabcontext to override selected_context, got %s", source)
	}
}

func TestContextStoreEmptyWorkspace(t *testing.T) {
	cs := NewContextStore("")

	// Test with empty workspace
	cs.UpdateFileSync("/absolute/path.go", "content", 1, "go", false)

	content, _, found := cs.GetFileContent("/absolute/path.go")
	if !found {
		t.Errorf("Expected to find absolute path even with empty workspace")
	}
	if content != "content" {
		t.Errorf("Expected content, got %s", content)
	}
}

func TestContextStoreWindowsPathNormalization(t *testing.T) {
	cs := NewContextStore("C:\\test\\workspace")

	// Test Windows path normalization
	cs.UpdateFileSync("C:\\test\\workspace\\main.go", "content", 1, "go", false)

	// Should be able to retrieve with forward slashes
	_, _, found := cs.GetFileContent("C:/test/workspace/main.go")
	if !found {
		t.Errorf("Expected to find file with forward slashes")
	}
}
