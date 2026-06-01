package cli_test

import (
	"bufio"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
)

// TestCmdMigrate_DryRun_Succeeds verifies that a dry-run from edge to small
// prints the plan header and the dry-run notice without error.
func TestCmdMigrate_DryRun_Succeeds(t *testing.T) {
	out, err := cli.CmdMigrate([]string{"--to", "small", "--dry-run", "--from", "edge"}, nil)
	if err != nil {
		t.Fatalf("CmdMigrate dry-run error: %v", err)
	}
	if !strings.Contains(out, "Migration: edge → small") {
		t.Errorf("expected 'Migration: edge → small' in output, got: %q", out)
	}
	if !strings.Contains(out, "(dry-run: no changes will be made)") {
		t.Errorf("expected dry-run notice in output, got: %q", out)
	}
}

// TestCmdMigrate_DowngradeRejected verifies that attempting to migrate from
// small to edge returns ErrDowngradeUnsupported.
func TestCmdMigrate_DowngradeRejected(t *testing.T) {
	// Provide "n" so that if somehow confirmation is reached we still cancel.
	reader := bufio.NewReader(strings.NewReader("n\n"))
	_, err := cli.CmdMigrate([]string{"--to", "edge", "--from", "small"}, reader)
	if err == nil {
		t.Fatal("expected error for downgrade, got nil")
	}
	if !strings.Contains(err.Error(), "downgrade") {
		t.Errorf("expected error to mention 'downgrade', got: %v", err)
	}
}

// TestCmdMigrate_MissingTo verifies that omitting --to returns an appropriate error.
func TestCmdMigrate_MissingTo(t *testing.T) {
	_, err := cli.CmdMigrate([]string{}, nil)
	if err == nil {
		t.Fatal("expected error when --to is missing, got nil")
	}
	if !strings.Contains(err.Error(), "--to is required") {
		t.Errorf("expected '--to is required' in error, got: %v", err)
	}
}

// TestCmdMigrate_MissingPostgresDSN_FailsFast verifies that a confirmed migration
// that requires a DB migration step fails when UBAG_POSTGRES_DSN is absent.
// The edge→small path includes a StepMigrateDB step.
func TestCmdMigrate_MissingPostgresDSN_FailsFast(t *testing.T) {
	// Unset DSN so the step fails with ErrMissingPrerequisite.
	t.Setenv("UBAG_POSTGRES_DSN", "")

	reader := bufio.NewReader(strings.NewReader("y\n"))
	// No --postgres-dsn flag, so it is empty.
	_, err := cli.CmdMigrate([]string{"--to", "small", "--from", "edge"}, reader)
	if err == nil {
		t.Fatal("expected error about missing Postgres DSN, got nil")
	}
	// The error chain should mention the missing prerequisite.
	if !strings.Contains(err.Error(), "UBAG_POSTGRES_DSN") && !strings.Contains(err.Error(), "migration failed") {
		t.Errorf("expected error mentioning UBAG_POSTGRES_DSN or 'migration failed', got: %v", err)
	}
}

// TestCmdMigrate_Cancelled verifies that answering "n" at the confirmation
// prompt cancels the migration without error.
func TestCmdMigrate_Cancelled(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("n\n"))
	out, err := cli.CmdMigrate([]string{"--to", "small", "--from", "edge"}, reader)
	if err != nil {
		t.Fatalf("unexpected error on cancel: %v", err)
	}
	if !strings.Contains(out, "cancelled") {
		t.Errorf("expected 'cancelled' in output, got: %q", out)
	}
}

// TestCmdMigrate_SameTier verifies same-tier migration is also rejected (toIdx <= fromIdx).
func TestCmdMigrate_SameTier(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("n\n"))
	_, err := cli.CmdMigrate([]string{"--to", "edge", "--from", "edge"}, reader)
	if err == nil {
		t.Fatal("expected error for same-tier migration, got nil")
	}
	if !strings.Contains(err.Error(), "downgrade") {
		t.Errorf("expected 'downgrade' in error, got: %v", err)
	}
}

// TestCmdMigrate_InvalidToProfile verifies that an unknown --to value errors.
func TestCmdMigrate_InvalidToProfile(t *testing.T) {
	_, err := cli.CmdMigrate([]string{"--to", "ultramax", "--from", "edge"}, nil)
	if err == nil {
		t.Fatal("expected error for unknown profile, got nil")
	}
}

// TestCmdMigrate_DryRun_EnvFromProfile verifies that when --from is absent,
// UBAG_PROFILE env is used as the source.
func TestCmdMigrate_DryRun_EnvFromProfile(t *testing.T) {
	t.Setenv("UBAG_PROFILE", "edge")
	out, err := cli.CmdMigrate([]string{"--to", "standard", "--dry-run"}, nil)
	if err != nil {
		t.Fatalf("CmdMigrate error: %v", err)
	}
	if !strings.Contains(out, "Migration: edge → standard") {
		t.Errorf("expected 'Migration: edge → standard', got: %q", out)
	}
}
