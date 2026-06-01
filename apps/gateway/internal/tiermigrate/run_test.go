package tiermigrate_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/profile"
	"github.com/ubag/ubag/apps/gateway/internal/tiermigrate"
)

func TestRun_DryRun_ChangesNothing(t *testing.T) {
	plan, err := tiermigrate.Plan(profile.Edge, profile.Small)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var buf bytes.Buffer
	opts := tiermigrate.RunOptions{
		DryRun: true,
		Output: &buf,
	}

	if err := tiermigrate.Run(context.Background(), plan, opts); err != nil {
		t.Fatalf("Run (dry-run) returned unexpected error: %v", err)
	}

	output := buf.String()

	// Every step must show the dry-run marker.
	if !strings.Contains(output, "(dry-run: skipped)") {
		t.Error("expected dry-run output to contain \"(dry-run: skipped)\"")
	}

	// Step descriptions must appear even in dry-run mode.
	for _, step := range plan.Steps {
		if !strings.Contains(output, step.Description) {
			t.Errorf("expected dry-run output to contain step description %q", step.Description)
		}
	}

	// "Migration complete" must NOT appear in dry-run mode.
	if strings.Contains(output, "Migration complete") {
		t.Error("dry-run output must not contain \"Migration complete\"")
	}
}

func TestRun_FailsFastOnError(t *testing.T) {
	// Build a plan that starts with StepMigrateDB (edge→small always has one).
	plan, err := tiermigrate.Plan(profile.Edge, profile.Small)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// Verify the plan actually has a StepMigrateDB as the first step.
	if len(plan.Steps) == 0 || plan.Steps[0].Kind != tiermigrate.StepMigrateDB {
		t.Skip("edge→small plan does not start with StepMigrateDB; test assumption changed")
	}

	var buf bytes.Buffer
	opts := tiermigrate.RunOptions{
		DryRun:      false,
		PostgresDSN: "", // deliberately missing → should fail on StepMigrateDB
		Output:      &buf,
	}

	runErr := tiermigrate.Run(context.Background(), plan, opts)
	if runErr == nil {
		t.Fatal("expected error when PostgresDSN is absent, got nil")
	}

	// Must fail with ErrMissingPrerequisite.
	var prereqErr *tiermigrate.ErrMissingPrerequisite
	if !errors.As(runErr, &prereqErr) {
		t.Errorf("expected *ErrMissingPrerequisite, got %T: %v", runErr, runErr)
	}

	// The step after StepMigrateDB must NOT have executed (fail-fast).
	// We know StepEnableCache comes after StepMigrateDB in edge→small.
	// A succeeded step prints "✓ done", so if any later step's kind appears
	// with that marker, fail-fast is broken.
	output := buf.String()
	if strings.Contains(output, "✓ done") {
		t.Error("fail-fast violated: a step printed '✓ done' after the failing step")
	}
}

func TestRun_MissingPrerequisite_EdgeToSmall(t *testing.T) {
	plan, err := tiermigrate.Plan(profile.Edge, profile.Small)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var buf bytes.Buffer
	opts := tiermigrate.RunOptions{
		DryRun:      false,
		PostgresDSN: "", // not provided
		Output:      &buf,
	}

	runErr := tiermigrate.Run(context.Background(), plan, opts)
	if runErr == nil {
		t.Fatal("expected ErrMissingPrerequisite, got nil")
	}

	var prereqErr *tiermigrate.ErrMissingPrerequisite
	if !errors.As(runErr, &prereqErr) {
		t.Fatalf("expected *ErrMissingPrerequisite, got %T: %v", runErr, runErr)
	}

	if prereqErr.StepKind != tiermigrate.StepMigrateDB {
		t.Errorf("ErrMissingPrerequisite.StepKind = %q, want %q", prereqErr.StepKind, tiermigrate.StepMigrateDB)
	}

	if prereqErr.Missing == "" {
		t.Error("ErrMissingPrerequisite.Missing must not be empty")
	}

	// Error message must mention the missing config name.
	if !strings.Contains(runErr.Error(), "UBAG_POSTGRES_DSN") {
		t.Errorf("error message %q should mention UBAG_POSTGRES_DSN", runErr.Error())
	}
}

func TestRun_AllSteps_Succeed(t *testing.T) {
	plan, err := tiermigrate.Plan(profile.Edge, profile.Small)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var buf bytes.Buffer
	opts := tiermigrate.RunOptions{
		DryRun:      false,
		PostgresDSN: "postgres://user:pass@localhost:5432/ubag", // provided → StepMigrateDB passes
		Output:      &buf,
	}

	if err := tiermigrate.Run(context.Background(), plan, opts); err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	output := buf.String()

	// Every step must show completion.
	wantDoneCount := len(plan.Steps)
	gotDoneCount := strings.Count(output, "✓ done")
	if gotDoneCount != wantDoneCount {
		t.Errorf("expected %d '✓ done' lines, got %d\nOutput:\n%s", wantDoneCount, gotDoneCount, output)
	}

	// Migration complete banner must be present.
	if !strings.Contains(output, "Migration complete") {
		t.Errorf("expected \"Migration complete\" in output\nOutput:\n%s", output)
	}
}
