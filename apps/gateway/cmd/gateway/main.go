package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ubag/ubag/apps/gateway/internal/serve"
)

func main() {
	if err := serve.Run(context.Background()); err != nil {
		slog.Error("gateway exited", "error", err)
		os.Exit(1)
	}
}
