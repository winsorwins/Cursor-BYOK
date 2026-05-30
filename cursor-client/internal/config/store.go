package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"cursor-client/internal/relay"
)

// UserConfig holds persisted user configuration.
type UserConfig struct {
	BaseURL       string                `json:"baseURL"`
	LicenseCode   string                `json:"licenseCode"`
	ModelAdapters []*relay.ModelAdapter `json:"modelAdapters"`
}

// DefaultUserConfig returns a new default config.
func DefaultUserConfig() *UserConfig {
	return &UserConfig{
		BaseURL:       "https://api2.cursor.sh",
		ModelAdapters: []*relay.ModelAdapter{},
	}
}

// Load reads configuration from path. Missing files return defaults.
func Load(path string) (*UserConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg := DefaultUserConfig()
			return cfg, Save(path, cfg)
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	cfg := DefaultUserConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api2.cursor.sh"
	}
	if cfg.ModelAdapters == nil {
		cfg.ModelAdapters = []*relay.ModelAdapter{}
	}
	normalizeModelAdapters(cfg.ModelAdapters)

	return cfg, nil
}

// Save writes configuration to path.
func Save(path string, cfg *UserConfig) error {
	if cfg == nil {
		cfg = DefaultUserConfig()
	}
	if cfg.ModelAdapters == nil {
		cfg.ModelAdapters = []*relay.ModelAdapter{}
	}
	normalizeModelAdapters(cfg.ModelAdapters)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	return nil
}

func normalizeModelAdapters(adapters []*relay.ModelAdapter) {
	for _, adapter := range adapters {
		if adapter != nil {
			adapter.Normalize()
		}
	}
}
