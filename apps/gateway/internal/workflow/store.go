// Package workflow provides definitions and runs for ordered multi-step
// workflows. Each step submits a job-like unit through a caller-injected
// dispatch callback, allowing the gateway to reuse its existing job-create
// path (payload policy + executor enqueue) without this package importing it.
//
// The package mirrors the structure of internal/webhooks and
// internal/templates: a Store interface with a MemoryStore (sync.Mutex) and a
// SQLiteStore (*sql.DB) implementation. Steps and step runs are persisted as
// JSON columns. All timestamps are stored in UTC.
package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Errors returned by the store and engine.
var (
	ErrNotFound        = errors.New("workflow not found")
	ErrScope           = errors.New("workflow tenant/app scope mismatch")
	ErrInvalidDef      = errors.New("invalid workflow definition")
	ErrInvalidRun      = errors.New("invalid workflow run")
	ErrStepsMismatch   = errors.New("workflow run steps do not match definition")
	ErrTerminalRun     = errors.New("workflow run is already in a terminal state")
	ErrDispatchMissing = errors.New("workflow dispatch callback is required")
)

// targetPattern restricts step targets to a conservative, lowercase allowlist.
var targetPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// RunState enumerates the lifecycle states of a run and its individual steps.
type RunState string

const (
	StatePending   RunState = "pending"
	StateRunning   RunState = "running"
	StateSucceeded RunState = "succeeded"
	StateFailed    RunState = "failed"
	StateCanceled  RunState = "canceled"
)

// TerminalState reports whether a run state is final.
func TerminalState(state RunState) bool {
	switch state {
	case StateSucceeded, StateFailed, StateCanceled:
		return true
	default:
		return false
	}
}

// Step is one ordered unit of work within a definition.
type Step struct {
	ID              string
	Target          string
	Command         string
	TemplateID      string
	Input           map[string]any
	ContinueOnError bool
	// DependsOn lists step IDs that must reach StateSucceeded before this
	// step is dispatched. An empty list means "depends on the prior step"
	// (preserving the original linear semantics).
	DependsOn []string
	// When is a CEL expression evaluated before dispatching this step. If the
	// expression evaluates to false the step is skipped (treated as succeeded).
	// An empty string means "always run".
	When string
	// Compensation, if non-nil, is dispatched when this step fails and all
	// retries are exhausted (saga compensating transaction).
	Compensation *Step
	// MaxRetries is the per-step retry ceiling (0 = no retry beyond the first
	// attempt). Retries use exponential backoff from the default policy.
	MaxRetries int
}

// Definition is an ordered template of steps scoped to a tenant and app.
type Definition struct {
	ID        string
	TenantID  string
	AppID     string
	Name      string
	Steps     []Step
	CreatedAt time.Time
}

// StepRun is the runtime record for a single step of a run.
type StepRun struct {
	StepID      string
	State       RunState
	JobID       string
	Error       string
	Retries     int
	StartedAt   time.Time
	CompletedAt time.Time
}

// Run is a single execution of a definition.
type Run struct {
	ID             string
	DefinitionID   string
	TenantID       string
	AppID          string
	State          RunState
	CurrentStep    int
	Steps          []StepRun
	IdempotencyKey string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Store persists workflow definitions and runs. Every method validates the
// tenant/app scope before returning data.
type Store interface {
	CreateDefinition(ctx context.Context, def Definition) (Definition, error)
	GetDefinition(ctx context.Context, tenantID string, appID string, id string) (Definition, bool, error)
	ListDefinitions(ctx context.Context, tenantID string, appID string, limit int) ([]Definition, error)
	CreateRun(ctx context.Context, run Run) (Run, error)
	GetRun(ctx context.Context, tenantID string, appID string, id string) (Run, bool, error)
	ListRuns(ctx context.Context, tenantID string, appID string, limit int) ([]Run, error)
	UpdateRun(ctx context.Context, run Run) error
}

const defaultListLimit = 100

func normalizeLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return defaultListLimit
	}
	return limit
}

// validateScope ensures both tenant and app identifiers are present.
func validateScope(tenantID string, appID string) error {
	if strings.TrimSpace(tenantID) == "" {
		return fmt.Errorf("%w: tenant_id is required", ErrScope)
	}
	if strings.TrimSpace(appID) == "" {
		return fmt.Errorf("%w: app_id is required", ErrScope)
	}
	return nil
}

// validateStep enforces the target allowlist, a non-empty command, and that
// per-step retry count is non-negative.
func validateStep(step Step) error {
	if strings.TrimSpace(step.ID) == "" {
		return fmt.Errorf("%w: step id is required", ErrInvalidDef)
	}
	if !targetPattern.MatchString(step.Target) {
		return fmt.Errorf("%w: step %q target %q must match %s", ErrInvalidDef, step.ID, step.Target, targetPattern.String())
	}
	if strings.TrimSpace(step.Command) == "" {
		return fmt.Errorf("%w: step %q command is required", ErrInvalidDef, step.ID)
	}
	if step.MaxRetries < 0 {
		return fmt.Errorf("%w: step %q max_retries must be >= 0", ErrInvalidDef, step.ID)
	}
	return nil
}

// validateDefinition validates scope, name, and every step.
func validateDefinition(def Definition) error {
	if err := validateScope(def.TenantID, def.AppID); err != nil {
		return err
	}
	if strings.TrimSpace(def.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidDef)
	}
	if len(def.Steps) == 0 {
		return fmt.Errorf("%w: at least one step is required", ErrInvalidDef)
	}
	seen := map[string]struct{}{}
	for _, step := range def.Steps {
		if err := validateStep(step); err != nil {
			return err
		}
		if _, dup := seen[step.ID]; dup {
			return fmt.Errorf("%w: duplicate step id %q", ErrInvalidDef, step.ID)
		}
		seen[step.ID] = struct{}{}
	}
	return nil
}

// validateRun validates scope and that a definition is referenced.
func validateRun(run Run) error {
	if err := validateScope(run.TenantID, run.AppID); err != nil {
		return err
	}
	if strings.TrimSpace(run.DefinitionID) == "" {
		return fmt.Errorf("%w: definition_id is required", ErrInvalidRun)
	}
	return nil
}

// newID returns a random, prefixed identifier.
func newID(prefix string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand should never fail; fall back to a timestamp-derived id.
		return prefix + "_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))[:24]
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

func cloneStep(in Step) Step {
	out := in
	out.Input = cloneMap(in.Input)
	if in.DependsOn != nil {
		out.DependsOn = make([]string, len(in.DependsOn))
		copy(out.DependsOn, in.DependsOn)
	}
	if in.Compensation != nil {
		comp := cloneStep(*in.Compensation)
		out.Compensation = &comp
	}
	return out
}

func cloneSteps(in []Step) []Step {
	if in == nil {
		return nil
	}
	out := make([]Step, len(in))
	for i, step := range in {
		out[i] = cloneStep(step)
	}
	return out
}

func cloneStepRuns(in []StepRun) []StepRun {
	if in == nil {
		return nil
	}
	out := make([]StepRun, len(in))
	copy(out, in)
	return out
}

func cloneDefinition(in Definition) Definition {
	out := in
	out.Steps = cloneSteps(in.Steps)
	return out
}

func cloneRun(in Run) Run {
	out := in
	out.Steps = cloneStepRuns(in.Steps)
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// NewStepRuns builds the initial pending StepRun slice for a definition. The
// wiring layer uses this when constructing a Run from a Definition.
func NewStepRuns(def Definition) []StepRun {
	runs := make([]StepRun, len(def.Steps))
	for i, step := range def.Steps {
		runs[i] = StepRun{StepID: step.ID, State: StatePending}
	}
	return runs
}
