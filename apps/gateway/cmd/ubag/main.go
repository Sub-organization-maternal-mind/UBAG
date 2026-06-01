package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
	"github.com/ubag/ubag/apps/gateway/internal/serve"
)

func main() {
	if len(os.Args) < 2 {
		out, _ := cli.Dispatch(os.Args[1:])
		fmt.Println(out)
		return
	}
	switch os.Args[1] {
	case "start":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		setEdgeDefaults()
		if err := serve.Run(ctx); err != nil {
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
		fmt.Println("TUI not yet implemented")
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
	defaults := map[string]string{
		"UBAG_PROFILE":                 "edge",
		"UBAG_GATEWAY_STORE":           "sqlite",
		"UBAG_EXECUTOR_MODE":           "file",
		"UBAG_WORKER_CONSUMER_ENABLED": "true",
		"UBAG_EXECUTOR_SPOOL_DIR":      edgeSpoolDir(),
		"UBAG_SQLITE_DSN":              edgeSQLiteDSN(),
	}
	for k, v := range defaults {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func edgeSpoolDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ubag", "spool")
}

func edgeSQLiteDSN() string {
	home, _ := os.UserHomeDir()
	db := filepath.Join(home, ".ubag", "gateway.db")
	return "file:" + db + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
}
