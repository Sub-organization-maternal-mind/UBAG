package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ubag/ubag/apps/gateway/internal/profile"
	"github.com/ubag/ubag/apps/gateway/internal/tiermigrate"
)

// CmdMigrate implements: ubag migrate --to <tier> [--dry-run] [--from <tier>]
//
// This command performs tier-level migrations (e.g. edge→small, small→enterprise).
// It is distinct from "ubag db-migrate", which is the schema-only SQL migration runner.
//
// input is the reader for confirmation prompts; if nil, os.Stdin is used (allows
// tests to inject a fake reader without touching os.Stdin).
func CmdMigrate(args []string, input *bufio.Reader) (string, error) {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	toTier     := fs.String("to", "", "Target tier (small|standard|enterprise)")
	fromTier   := fs.String("from", "", "Source tier (default: current UBAG_PROFILE)")
	dryRun     := fs.Bool("dry-run", false, "Print plan without executing")
	postgresDSN := fs.String("postgres-dsn", "", "Postgres DSN (required for DB migration steps)")

	if err := fs.Parse(args); err != nil {
		return "", err
	}

	if *toTier == "" {
		return "", fmt.Errorf("--to is required")
	}

	// Resolve from profile.
	rawFrom := *fromTier
	if rawFrom == "" {
		rawFrom = os.Getenv("UBAG_PROFILE")
	}
	fromProfile, err := profile.ParseOrDefault(rawFrom)
	if err != nil {
		return "", fmt.Errorf("invalid source profile: %w", err)
	}

	toProfile, err := profile.Parse(*toTier)
	if err != nil {
		return "", fmt.Errorf("invalid target profile: %w", err)
	}

	// Build plan.
	plan, err := tiermigrate.Plan(fromProfile, toProfile)
	if err != nil {
		return "", err
	}

	// Print plan.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Migration: %s → %s (%d steps)\n", fromProfile, toProfile, len(plan.Steps)))
	for i, step := range plan.Steps {
		sb.WriteString(fmt.Sprintf("  [%d] %s: %s\n", i+1, step.Kind, step.Description))
	}

	if *dryRun {
		sb.WriteString("\n(dry-run: no changes will be made)\n")
		return sb.String(), nil
	}

	// Confirmation gate.
	if input == nil {
		input = bufio.NewReader(os.Stdin)
	}
	fmt.Print("Proceed? [y/N]: ")
	answer, _ := input.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		return "Migration cancelled.\n", nil
	}

	// Execute.
	opts := tiermigrate.RunOptions{
		DryRun:      false,
		PostgresDSN: *postgresDSN,
		Output:      os.Stdout,
	}
	if err := tiermigrate.Run(context.Background(), plan, opts); err != nil {
		return "", fmt.Errorf("migration failed: %w", err)
	}
	return "Migration complete.\n", nil
}
