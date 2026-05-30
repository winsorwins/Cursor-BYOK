package cursor

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	localstatsig "cursor-client/internal/statsig"

	_ "modernc.org/sqlite"
)

const statsigBootstrapStorageKey = "workbench.experiments.statsigBootstrap"

var adminSettingsStorageKeys = []string{
	"adminSettings.cached",
	"autorun.cachedAdminSettings",
}

// RepairStatsigBootstrapCache updates Cursor's persisted Statsig bootstrap so
// extensions that start before the network bootstrap do not read stale values.
func RepairStatsigBootstrapCache() (bool, error) {
	db, err := openGlobalStorageDB()
	if err != nil {
		return false, err
	}
	if db == nil {
		return false, nil
	}
	defer db.Close()

	var existing string
	err = db.QueryRow("select value from ItemTable where key = ?", statsigBootstrapStorageKey).Scan(&existing)
	if err == nil && localstatsig.HasUsableHTTP2PingConfig(existing) {
		return false, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("failed to read Cursor statsig bootstrap: %w", err)
	}

	config := localstatsig.BuildBootstrapConfig("", time.Now().UnixMilli())
	_, err = db.Exec(
		"insert into ItemTable(key, value) values(?, ?) on conflict(key) do update set value = excluded.value",
		statsigBootstrapStorageKey,
		config,
	)
	if err != nil {
		return false, fmt.Errorf("failed to write Cursor statsig bootstrap: %w", err)
	}
	return true, nil
}

// RepairAdminSettingsCache removes cached admin model allowlists so local BYOK
// catalog models are not treated as blocked by stale policy state.
func RepairAdminSettingsCache() (bool, error) {
	db, err := openGlobalStorageDB()
	if err != nil {
		return false, err
	}
	if db == nil {
		return false, nil
	}
	defer db.Close()

	clean := `{"allowedModels":[],"blockedModels":[],"dotCursorProtection":false,"browserFeatures":false,"browserOriginAllowlist":[],"byokDisabled":false,"networkDenylist":[],"networkAllowlist":[],"cloudAgentEgressAllowlist":[]}`
	repaired := false
	for _, key := range adminSettingsStorageKeys {
		var existing string
		err := db.QueryRow("select value from ItemTable where key = ?", key).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return repaired, fmt.Errorf("failed to read Cursor admin settings cache: %w", err)
		}
		if existing == clean {
			continue
		}
		if _, err := db.Exec("update ItemTable set value = ? where key = ?", clean, key); err != nil {
			return repaired, fmt.Errorf("failed to write Cursor admin settings cache: %w", err)
		}
		repaired = true
	}
	return repaired, nil
}

func RepairStartupCaches() (bool, bool, error) {
	statsigRepaired, err := RepairStatsigBootstrapCache()
	if err != nil {
		return false, false, err
	}
	adminRepaired, err := RepairAdminSettingsCache()
	if err != nil {
		return statsigRepaired, false, err
	}
	return statsigRepaired, adminRepaired, nil
}

func openGlobalStorageDB() (*sql.DB, error) {
	dbPath, err := defaultGlobalStorageDBPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to stat Cursor state db: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open Cursor state db: %w", err)
	}

	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to configure Cursor state db: %w", err)
	}
	return db, nil
}

func defaultGlobalStorageDBPath() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(base, "Cursor", "User", "globalStorage", "state.vscdb"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to find home dir: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb"), nil
	default:
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("failed to find config dir: %w", err)
		}
		return filepath.Join(configDir, "Cursor", "User", "globalStorage", "state.vscdb"), nil
	}
}
