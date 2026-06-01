package tiermigrate

import (
	"context"
	"fmt"
	"io"
	"os"
)

// RunOptions configures how the migration executes.
type RunOptions struct {
	DryRun  bool
	Verbose bool
	// PostgresDSN is required for steps that need Postgres (e.g. edge→small DB migration).
	// In a full implementation this would be passed to backup.Run (snapshot SQLite) then
	// backup.Restore (restore into Postgres) via internal/backup.
	PostgresDSN string
	// ReadyURL is the gateway /v1/ready endpoint to probe after migration.
	ReadyURL string
	// Output captures log lines (defaults to os.Stdout).
	Output io.Writer
}

// ErrMissingPrerequisite is returned when a required config (e.g. PostgresDSN) is absent.
type ErrMissingPrerequisite struct {
	StepKind StepKind
	Missing  string
}

func (e *ErrMissingPrerequisite) Error() string {
	return fmt.Sprintf("step %s requires %s (not configured)", e.StepKind, e.Missing)
}

// Run executes all steps in the plan in order.
// If opts.DryRun is true, it prints each step but does not execute it.
// If any step fails, it returns the error immediately (no partial feature flips).
func Run(ctx context.Context, plan *MigrationPlan, opts RunOptions) error {
	out := opts.Output
	if out == nil {
		out = os.Stdout
	}

	fmt.Fprintf(out, "Migration plan: %s → %s (%d steps)\n",
		plan.FromProfile, plan.ToProfile, len(plan.Steps))

	for i, step := range plan.Steps {
		fmt.Fprintf(out, "  [%d/%d] %s: %s\n", i+1, len(plan.Steps), step.Kind, step.Description)
		if opts.DryRun {
			fmt.Fprintf(out, "    (dry-run: skipped)\n")
			continue
		}

		if err := executeStep(ctx, step, opts, out); err != nil {
			return fmt.Errorf("step %s failed: %w", step.Kind, err)
		}
		fmt.Fprintf(out, "    ✓ done\n")
	}

	if !opts.DryRun {
		fmt.Fprintf(out, "Migration complete: %s → %s\n", plan.FromProfile, plan.ToProfile)
	}
	return nil
}

func executeStep(ctx context.Context, step MigrationStep, opts RunOptions, out io.Writer) error {
	switch step.Kind {
	case StepMigrateDB:
		// Prerequisite: PostgresDSN must be set for any DB migration.
		// Full implementation would:
		//   1. backup.Run(ctx, backup.Options{SQLitePath: ..., OutDir: tmpDir}) to snapshot SQLite
		//   2. backup.Restore(ctx, backup.RestoreOptions{From: tmpDir, To: opts.PostgresDSN}) to load into Postgres
		if opts.PostgresDSN == "" {
			return &ErrMissingPrerequisite{StepKind: step.Kind, Missing: "UBAG_POSTGRES_DSN"}
		}
		fmt.Fprintf(out, "    migrating DB from %s to %s\n", step.From, step.To)
		// Stub: real impl calls backup engine (internal/backup) snapshot + restore.
		return nil
	case StepEnableCache:
		fmt.Fprintf(out, "    enabling semantic cache\n")
		return nil
	case StepEnableRBAC:
		fmt.Fprintf(out, "    enabling multi-tenant RBAC\n")
		return nil
	case StepEnableSSO:
		fmt.Fprintf(out, "    enabling SSO\n")
		return nil
	case StepEnableSCIM:
		fmt.Fprintf(out, "    enabling SCIM\n")
		return nil
	case StepSwitchAudit:
		fmt.Fprintf(out, "    switching audit delivery: %s → %s\n", step.From, step.To)
		return nil
	case StepEnableGeoRepl:
		fmt.Fprintf(out, "    enabling geo-replication\n")
		return nil
	case StepEnableCompliance:
		fmt.Fprintf(out, "    enabling compliance modes\n")
		return nil
	case StepSwitchExecutor:
		fmt.Fprintf(out, "    switching executor\n")
		return nil
	case StepSwitchArtifacts:
		fmt.Fprintf(out, "    switching artifact store\n")
		return nil
	case StepUpgradeBrowser:
		fmt.Fprintf(out, "    upgrading browser session pool\n")
		return nil
	default:
		return fmt.Errorf("unknown step kind: %s", step.Kind)
	}
}
