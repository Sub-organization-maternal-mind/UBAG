package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ubag/ubag/apps/gateway/internal/cli"
	"github.com/ubag/ubag/apps/gateway/internal/cli/tui"
	"github.com/ubag/ubag/apps/gateway/internal/serve"
)

func main() {
	if len(os.Args) < 2 {
		out, err := cli.Dispatch(os.Args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(out)
		return
	}
	switch os.Args[1] {
	case "start":
		setEdgeDefaults()
		if err := serve.Run(context.Background()); err != nil {
			slog.Error("ubag start exited", "error", err)
			os.Exit(1)
		}
	case "init":
		if err := cli.EdgeInit(); err != nil {
			fmt.Fprintln(os.Stderr, "ubag init:", err)
			os.Exit(1)
		}
		fmt.Println("Edge config initialized in ~/.ubag/")
	case "tui":
		cfg, err := cli.LoadConfig()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error loading config:", err)
			os.Exit(1)
		}
		client := cli.NewClient(cfg.BaseURL, cfg.AppSecret, cfg.APIVersion)
		model := tui.New(client)
		p := tea.NewProgram(model, tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "tui error:", err)
			os.Exit(1)
		}
	default:
		out, err := cli.Dispatch(os.Args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(out)
	}
}

// setEdgeDefaults sets edge-profile environment defaults if not already set.
// This enables `ubag start` to boot in single-process mode without any config.
func setEdgeDefaults() {
	spoolDir, err := edgeSpoolDir()
	if err != nil {
		slog.Warn("edge: cannot determine spool dir", "error", err)
		spoolDir = ".ubag-spool"
	}
	sqliteDSN, err := edgeSQLiteDSN()
	if err != nil {
		slog.Warn("edge: cannot determine sqlite DSN", "error", err)
		sqliteDSN = "file:ubag-gateway.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	}
	defaults := map[string]string{
		"UBAG_PROFILE":                 "edge",
		"UBAG_GATEWAY_STORE":           "sqlite",
		"UBAG_EXECUTOR_MODE":           "file",
		"UBAG_WORKER_CONSUMER_ENABLED": "true",
		"UBAG_EXECUTOR_SPOOL_DIR":      spoolDir,
		"UBAG_SQLITE_DSN":              sqliteDSN,
	}
	for k, v := range defaults {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func edgeSpoolDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".ubag", "spool"), nil
}

func edgeSQLiteDSN() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	db := filepath.Join(home, ".ubag", "gateway.db")
	return "file:" + db + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", nil
}
