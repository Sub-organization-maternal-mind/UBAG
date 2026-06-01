package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// EdgeInit writes default edge config files to ~/.ubag/.
// Creates: ~/.ubag/config.json (CLI config) and ~/.ubag/.env.example (shell env hints).
func EdgeInit() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}
	ubagDir := filepath.Join(home, ".ubag")
	if err := os.MkdirAll(ubagDir, 0700); err != nil {
		return fmt.Errorf("create ~/.ubag: %w", err)
	}
	// Write CLI config.
	cfg := Config{
		BaseURL:    "http://localhost:8080",
		AppSecret:  "",
		APIVersion: DefaultAPIVersion,
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	cfgPath := filepath.Join(ubagDir, "config.json")
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// Write a hint .env file (informational only, not auto-loaded).
	envHints := "# Edge profile environment hints\n" +
		"UBAG_PROFILE=edge\n" +
		"UBAG_GATEWAY_STORE=sqlite\n" +
		"UBAG_EXECUTOR_MODE=file\n" +
		"UBAG_WORKER_CONSUMER_ENABLED=true\n"
	envPath := filepath.Join(ubagDir, ".env.example")
	if err := os.WriteFile(envPath, []byte(envHints), 0600); err != nil {
		return fmt.Errorf("write env example: %w", err)
	}
	return nil
}

// DashboardOpen opens the UBAG dashboard in the system browser.
func DashboardOpen(baseURL string) error {
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	url := baseURL + "/dashboard"
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
