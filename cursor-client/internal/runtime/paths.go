package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const appDirName = "Cursor助手"

// Paths contains all local file-system locations used by the application.
type Paths struct {
	DataDir      string
	ConfigPath   string
	CursorBackup string
	CertDir      string
	CACertPath   string
	CAKeyPath    string
	LogDir       string
}

// ResolvePaths returns platform-specific application paths and ensures the
// required directories exist.
func ResolvePaths() (*Paths, error) {
	dataDir, err := dataDir()
	if err != nil {
		return nil, err
	}

	paths := &Paths{
		DataDir:      dataDir,
		ConfigPath:   filepath.Join(dataDir, "config.json"),
		CursorBackup: filepath.Join(dataDir, "cursor-settings.backup.json"),
		CertDir:      filepath.Join(dataDir, "certs"),
		CACertPath:   filepath.Join(dataDir, "certs", "ca.crt"),
		CAKeyPath:    filepath.Join(dataDir, "certs", "ca.key"),
		LogDir:       filepath.Join(dataDir, "logs"),
	}

	for _, dir := range []string{paths.DataDir, paths.CertDir, paths.LogDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	return paths, nil
}

func dataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(base, appDirName), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to find home dir: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", appDirName), nil
	default:
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("failed to find config dir: %w", err)
		}
		return filepath.Join(configDir, appDirName), nil
	}
}
