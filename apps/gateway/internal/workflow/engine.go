package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DispatchFunc submits a single step as a job-like unit and returns the
// resulting job identifier. The gateway wiring implements this by reusing its
// existing job-create path (payload policy validation + executor enqueue).
type DispatchFunc func(step Step) (jobID string, err error)

// Engine drives the run state machine over a Store. It is safe for concurrent
// use: Advance and Cancel are serialized with an internal mutex so a single
// run is advanced deterministically.
type Engine struct {
	store Store
	now   func() time.Time
	mu    sync.Mutex
}

// NewEngine constructs an Engine backed by the supplied store.
func NewEngine(store Store) *Engine {
	return &Engine{store: store, now: time.Now}
}

// Advance drives the run forward using a topologically-sorted DAG execution
// strategy:
//
//  1. Steps are sorted by DependsOn before execution; empty DependsOn falls
//     back to the original linear order (backward-compatible).
//  2. Before dispatching, a CEL when: expression is evaluated; a false result
//     skips the step (treated as succeeded).
//  3. On dispatch failure, per-step retries are attempted up to MaxRetries.
//  4. After all retries are exhausted, if a compensation step is defined it is
//     dispatched before marking the run as failed.
//  5. ContinueOnError overrides all of the above and allows execution to
//     continue despite a failure.
func (e *Engine) Advance(ctx context.Context, run *Run, dispatch DispatchFunc) error {
	if run == nil {
		return fmt.Errorf("%w: run is nil", ErrInvalidRun)
	}
	if dispatch == nil {
		return ErrDispatchMissing
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateRun(*run); err != nil {
		return err
	}
	if TerminalState(run.State) {
		return fmt.Errorf("%w: state %s", ErrTerminalRun, run.State)
	}

	def, found, err := e.store.GetDefinition(ctx, run.TenantID, run.AppID, run.DefinitionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: definition %s", ErrNotFound, run.DefinitionID)
	}

	// Compute topological execution order (validates the DAG, detects cycles).
	topoOrder, err := topoSort(def)
	if err != nil {
		return err
	}

	// Initialize step-run slice on first advance.
	if len(run.Steps) == 0 {
		run.Steps = NewStepRuns(def)
	}
	if len(run.Steps) != len(def.Steps) {
		return ErrStepsMismatch
	}
	for i := range run.Steps {
		if run.Steps[i].StepID != def.Steps[i].ID {
			return ErrStepsMismatch
		}
	}

	run.State = StateRunning
	if err := e.persist(ctx, run); err != nil {
		return err
	}

	for run.CurrentStep < len(topoOrder) {
		idx := topoOrder[run.CurrentStep]
		step := def.Steps[idx]
		stepRun := &run.Steps[idx]

		// Skip steps already resolved (idempotent re-advance).
		if stepRun.State == StateSucceeded {
			run.CurrentStep++
			continue
		}

		// Evaluate CEL when: condition; skip if false.
		if step.When != "" {
			ok, celErr := evalWhen(step.When, run, def)
			if celErr != nil {
				// CEL error is treated as a permanent step failure.
				stepRun.State = StateFailed
				stepRun.Error = celErr.Error()
				if step.ContinueOnError {
					run.CurrentStep++
					if err := e.persist(ctx, run); err != nil {
						return err
					}
					continue
				}
				run.State = StateFailed
				return e.persist(ctx, run)
			}
			if !ok {
				// Condition false → skip (success).
				stepRun.State = StateSucceeded
				run.CurrentStep++
				if err := e.persist(ctx, run); err != nil {
					return err
				}
				continue
			}
		}

		// Dispatch with per-step retries.
		dispatchErr := e.dispatchWithRetries(ctx, run, step, stepRun, dispatch)

		if dispatchErr == nil {
			run.CurrentStep++
			if err := e.persist(ctx, run); err != nil {
				return err
			}
			continue
		}

		// Step failed after all retries.
		if step.ContinueOnError {
			run.CurrentStep++
			if err := e.persist(ctx, run); err != nil {
				return err
			}
			continue
		}

		// Run compensation step if defined.
		if step.Compensation != nil {
			_, _ = dispatch(*step.Compensation) // best-effort; ignore compensation errors
		}

		run.State = StateFailed
		return e.persist(ctx, run)
	}

	run.State = StateSucceeded
	return e.persist(ctx, run)
}

// dispatchWithRetries dispatches a step and retries up to step.MaxRetries
// times. It mutates stepRun in place and persists after each attempt.
func (e *Engine) dispatchWithRetries(
	ctx context.Context,
	run *Run,
	step Step,
	stepRun *StepRun,
	dispatch DispatchFunc,
) error {
	maxTries := step.MaxRetries + 1 // MaxRetries=0 means exactly 1 attempt

	for attempt := 0; attempt < maxTries; attempt++ {
		stepRun.State = StateRunning
		stepRun.StartedAt = e.now().UTC()
		stepRun.Error = ""
		if err := e.persist(ctx, run); err != nil {
			return err
		}

		jobID, dispatchErr := dispatch(step)
		stepRun.CompletedAt = e.now().UTC()

		if dispatchErr == nil {
			stepRun.JobID = jobID
			stepRun.State = StateSucceeded
			if err := e.persist(ctx, run); err != nil {
				return err
			}
			return nil
		}

		stepRun.State = StateFailed
		stepRun.Error = dispatchErr.Error()
		stepRun.Retries = attempt + 1

		if attempt < maxTries-1 {
			// More retries available; reset to pending for the next iteration.
			stepRun.State = StatePending
		}
		if err := e.persist(ctx, run); err != nil {
			return err
		}

		if attempt == maxTries-1 {
			return dispatchErr
		}
	}
	return nil // unreachable
}

// Cancel marks a non-terminal run as canceled and persists the change.
func (e *Engine) Cancel(ctx context.Context, run *Run) error {
	if run == nil {
		return fmt.Errorf("%w: run is nil", ErrInvalidRun)
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := validateRun(*run); err != nil {
		return err
	}
	if TerminalState(run.State) {
		return fmt.Errorf("%w: state %s", ErrTerminalRun, run.State)
	}
	run.State = StateCanceled
	now := e.now().UTC()
	for i := range run.Steps {
		if !TerminalState(run.Steps[i].State) {
			run.Steps[i].State = StateCanceled
			if run.Steps[i].CompletedAt.IsZero() {
				run.Steps[i].CompletedAt = now
			}
		}
	}
	return e.persist(ctx, run)
}

func (e *Engine) persist(ctx context.Context, run *Run) error {
	run.UpdatedAt = e.now().UTC()
	return e.store.UpdateRun(ctx, *run)
}
