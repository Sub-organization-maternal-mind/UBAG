package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	// DefaultAPIVersion matches the server's default.
	DefaultAPIVersion = "2026-05-22"
	// DefaultBaseURL is used when no config file exists.
	DefaultBaseURL = "http://localhost:8080"

	configFileName = "config.json"
	configDirName  = ".ubag"
)

// Config holds persisted CLI configuration.
type Config struct {
	BaseURL    string `json:"base_url"`
	AppSecret  string `json:"app_secret"`
	APIVersion string `json:"api_version"`
}

// configPath is the resolved path to config.json.  Tests override this via
// SetConfigPath so they can write to t.TempDir() without touching the real
// home directory.
var configPath string

func init() {
	configPath = defaultConfigPath()
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(configDirName, configFileName)
	}
	return filepath.Join(home, configDirName, configFileName)
}

// SetConfigPath overrides the path used by LoadConfig / SaveConfig.
// Intended for tests only.
func SetConfigPath(p string) {
	configPath = p
}

// LoadConfig reads ~/.ubag/config.json and returns a Config.
// If the file does not exist the returned Config has sensible defaults.
func LoadConfig() (Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{
				BaseURL:    DefaultBaseURL,
				APIVersion: DefaultAPIVersion,
			}, nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	// Back-fill defaults for fields that were omitted in older config files.
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = DefaultAPIVersion
	}
	return cfg, nil
}

// SaveConfig writes cfg to ~/.ubag/config.json atomically.
// The file and its parent directory are created if they do not exist.
// Permissions are set to 0600 (owner read/write only).
func SaveConfig(cfg Config) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file in the same directory then rename for atomicity.
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, configPath)
}
