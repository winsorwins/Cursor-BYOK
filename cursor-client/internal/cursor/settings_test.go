package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsManagerUsesTemporaryPath(t *testing.T) {
	// Create temporary APPDATA directory
	tempDir := t.TempDir()
	t.Setenv("APPDATA", tempDir)

	// Create temporary Cursor settings directory
	settingsDir := filepath.Join(tempDir, "Cursor", "User")
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatalf("failed to create temp settings dir: %v", err)
	}

	// Create initial settings file
	settingsPath := filepath.Join(settingsDir, "settings.json")
	initialSettings := map[string]any{"existing": "value"}
	data, _ := json.Marshal(initialSettings)
	if err := os.WriteFile(settingsPath, data, 0600); err != nil {
		t.Fatalf("failed to write initial settings: %v", err)
	}

	// Create backup directory
	backupDir := filepath.Join(tempDir, "backup")
	backupPath := filepath.Join(backupDir, "settings.json.bak")

	// Create SettingsManager
	manager, err := NewSettingsManager(backupPath)
	if err != nil {
		t.Fatalf("NewSettingsManager failed: %v", err)
	}

	// Verify it uses the temporary path
	if manager.SettingsPath() != settingsPath {
		t.Errorf("SettingsPath = %q, want %q", manager.SettingsPath(), settingsPath)
	}

	// Apply proxy settings
	proxyURL := "http://127.0.0.1:18080"
	if err := manager.ApplyProxy(proxyURL); err != nil {
		t.Fatalf("ApplyProxy failed: %v", err)
	}

	// Verify settings were written to temporary path
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("failed to read settings after ApplyProxy: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("failed to parse settings: %v", err)
	}

	if settings[proxyKey] != proxyURL {
		t.Errorf("proxy setting = %v, want %q", settings[proxyKey], proxyURL)
	}

	// Verify backup was created in temporary path
	if _, err := os.Stat(backupPath); err != nil {
		t.Errorf("backup not created at %q: %v", backupPath, err)
	}

	// Verify UsesProxy reads from temporary path
	usesProxy, err := manager.UsesProxy(proxyURL)
	if err != nil {
		t.Fatalf("UsesProxy failed: %v", err)
	}
	if !usesProxy {
		t.Error("UsesProxy = false, want true")
	}

	// Verify RestoreProxy restores from temporary backup
	if err := manager.RestoreProxy(); err != nil {
		t.Fatalf("RestoreProxy failed: %v", err)
	}

	raw, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("failed to read settings after RestoreProxy: %v", err)
	}

	var restored map[string]any
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("failed to parse restored settings: %v", err)
	}

	if restored["existing"] != "value" {
		t.Errorf("restored settings missing original value")
	}
}
