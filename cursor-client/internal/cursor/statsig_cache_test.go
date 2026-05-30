package cursor

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRepairStatsigBootstrapCacheUsesTemporaryDB(t *testing.T) {
	// Create temporary APPDATA directory
	tempDir := t.TempDir()
	t.Setenv("APPDATA", tempDir)

	// Create temporary globalStorage directory
	globalStorageDir := filepath.Join(tempDir, "Cursor", "User", "globalStorage")
	if err := os.MkdirAll(globalStorageDir, 0700); err != nil {
		t.Fatalf("failed to create temp globalStorage dir: %v", err)
	}

	// Create temporary state.vscdb
	dbPath := filepath.Join(globalStorageDir, "state.vscdb")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to create temp db: %v", err)
	}

	// Create ItemTable
	_, err = db.Exec("CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)")
	if err != nil {
		db.Close()
		t.Fatalf("failed to create ItemTable: %v", err)
	}

	// Insert old statsig bootstrap value
	oldValue := `{"feature_gates":{}}`
	_, err = db.Exec("INSERT INTO ItemTable (key, value) VALUES (?, ?)", statsigBootstrapStorageKey, oldValue)
	if err != nil {
		db.Close()
		t.Fatalf("failed to insert old value: %v", err)
	}
	db.Close()

	// Call RepairStatsigBootstrapCache
	repaired, err := RepairStatsigBootstrapCache()
	if err != nil {
		t.Fatalf("RepairStatsigBootstrapCache failed: %v", err)
	}

	if !repaired {
		t.Error("expected repaired=true")
	}

	// Verify the value was updated in temporary DB
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	defer db.Close()

	var newValue string
	err = db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", statsigBootstrapStorageKey).Scan(&newValue)
	if err != nil {
		t.Fatalf("failed to read updated value: %v", err)
	}

	if newValue == oldValue {
		t.Error("statsig bootstrap value was not updated")
	}

	// Verify the DB path is in tempDir
	if !filepath.HasPrefix(dbPath, tempDir) {
		t.Errorf("DB path %q is not in tempDir %q", dbPath, tempDir)
	}
}

func TestRepairAdminSettingsCacheUsesTemporaryDB(t *testing.T) {
	// Create temporary APPDATA directory
	tempDir := t.TempDir()
	t.Setenv("APPDATA", tempDir)

	// Create temporary globalStorage directory
	globalStorageDir := filepath.Join(tempDir, "Cursor", "User", "globalStorage")
	if err := os.MkdirAll(globalStorageDir, 0700); err != nil {
		t.Fatalf("failed to create temp globalStorage dir: %v", err)
	}

	// Create temporary state.vscdb
	dbPath := filepath.Join(globalStorageDir, "state.vscdb")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to create temp db: %v", err)
	}

	// Create ItemTable
	_, err = db.Exec("CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)")
	if err != nil {
		db.Close()
		t.Fatalf("failed to create ItemTable: %v", err)
	}

	// Insert old admin settings values
	oldValue := `{"allowedModels":["old-model"],"blockedModels":[]}`
	for _, key := range adminSettingsStorageKeys {
		_, err = db.Exec("INSERT INTO ItemTable (key, value) VALUES (?, ?)", key, oldValue)
		if err != nil {
			db.Close()
			t.Fatalf("failed to insert old value for %s: %v", key, err)
		}
	}
	db.Close()

	// Call RepairAdminSettingsCache
	repaired, err := RepairAdminSettingsCache()
	if err != nil {
		t.Fatalf("RepairAdminSettingsCache failed: %v", err)
	}

	if !repaired {
		t.Error("expected repaired=true")
	}

	// Verify the values were updated in temporary DB
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	defer db.Close()

	cleanValue := `{"allowedModels":[],"blockedModels":[],"dotCursorProtection":false,"browserFeatures":false,"browserOriginAllowlist":[],"byokDisabled":false,"networkDenylist":[],"networkAllowlist":[],"cloudAgentEgressAllowlist":[]}`
	for _, key := range adminSettingsStorageKeys {
		var newValue string
		err = db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", key).Scan(&newValue)
		if err != nil {
			t.Fatalf("failed to read updated value for %s: %v", key, err)
		}

		if newValue != cleanValue {
			t.Errorf("admin settings %s = %q, want %q", key, newValue, cleanValue)
		}
	}

	// Verify the DB path is in tempDir
	if !filepath.HasPrefix(dbPath, tempDir) {
		t.Errorf("DB path %q is not in tempDir %q", dbPath, tempDir)
	}
}
