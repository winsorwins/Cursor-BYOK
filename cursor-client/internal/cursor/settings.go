package cursor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	disableHTTP2Key                  = "cursor.general.disableHttp2"
	systemCertificatesV2Key          = "http.experimental.systemCertificatesV2"
	proxyKey                         = "http.proxy"
	proxyKerberosServicePrincipalKey = "http.proxyKerberosServicePrincipal"
	proxyStrictSSLKey                = "http.proxyStrictSSL"
)

// SettingsManager reads, patches, and restores Cursor user settings.
type SettingsManager struct {
	settingsPath string
	backupPath   string
}

// NewSettingsManager creates a manager using the platform default Cursor
// settings path.
func NewSettingsManager(backupPath string) (*SettingsManager, error) {
	path, err := defaultSettingsPath()
	if err != nil {
		return nil, err
	}
	return &SettingsManager{settingsPath: path, backupPath: backupPath}, nil
}

// SettingsPath returns the active Cursor settings path.
func (m *SettingsManager) SettingsPath() string {
	return m.settingsPath
}

// ApplyProxy stores a backup and writes proxy settings for Cursor.
func (m *SettingsManager) ApplyProxy(proxyURL string) error {
	settings, raw, err := m.readSettings()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.settingsPath), 0700); err != nil {
		return fmt.Errorf("failed to create Cursor settings dir: %w", err)
	}
	if err := m.ensureBackup(raw); err != nil {
		return err
	}

	settings[disableHTTP2Key] = true
	settings[systemCertificatesV2Key] = true
	settings[proxyKey] = proxyURL
	settings[proxyKerberosServicePrincipalKey] = proxyURL
	settings[proxyStrictSSLKey] = false

	return writeJSON(m.settingsPath, settings)
}

// UsesProxy reports whether Cursor is currently configured to use proxyURL.
func (m *SettingsManager) UsesProxy(proxyURL string) (bool, error) {
	settings, _, err := m.readSettings()
	if err != nil {
		return false, err
	}
	value, _ := settings[proxyKey].(string)
	return value == proxyURL, nil
}

// RestoreProxy restores Cursor settings from backup, if present.
func (m *SettingsManager) RestoreProxy() error {
	backup, err := os.ReadFile(m.backupPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("failed to read Cursor settings backup: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.settingsPath), 0700); err != nil {
		return fmt.Errorf("failed to create Cursor settings dir: %w", err)
	}
	if err := os.WriteFile(m.settingsPath, backup, 0600); err != nil {
		return fmt.Errorf("failed to restore Cursor settings: %w", err)
	}
	return nil
}

func (m *SettingsManager) readSettings() (map[string]any, []byte, error) {
	raw, err := os.ReadFile(m.settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, []byte("{}"), nil
		}
		return nil, nil, fmt.Errorf("failed to read Cursor settings: %w", err)
	}

	settings := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, nil, fmt.Errorf("Cursor settings is not valid JSON: %w", err)
		}
	}
	return settings, raw, nil
}

func (m *SettingsManager) ensureBackup(raw []byte) error {
	if _, err := os.Stat(m.backupPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to check Cursor settings backup: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.backupPath), 0700); err != nil {
		return fmt.Errorf("failed to create backup dir: %w", err)
	}
	if err := os.WriteFile(m.backupPath, raw, 0600); err != nil {
		return fmt.Errorf("failed to write Cursor settings backup: %w", err)
	}
	return nil
}

func defaultSettingsPath() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(base, "Cursor", "User", "settings.json"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to find home dir: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "settings.json"), nil
	default:
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("failed to find config dir: %w", err)
		}
		return filepath.Join(configDir, "Cursor", "User", "settings.json"), nil
	}
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
