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

// Advance drives the run forward from its current step until the run reaches a
// terminal state or a non-continue-on-error step fails. It marks the run
// running, dispatches each remaining step via the callback, records the
// returned job id, advances on success, fails (or continues when
// ContinueOnError is set) on error, and persists every transition through the
// store. The supplied run pointer is mutated in place to reflect the new state.
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

	// Initialize the step-run slice on first advance.
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

	for run.CurrentStep < len(def.Steps) {
		idx := run.CurrentStep
		step := def.Steps[idx]
		stepRun := &run.Steps[idx]

		// Skip steps already resolved (idempotent re-advance).
		if stepRun.State == StateSucceeded {
			run.CurrentStep++
			continue
		}

		stepRun.State = StateRunning
		stepRun.StartedAt = e.now().UTC()
		stepRun.Error = ""
		if err := e.persist(ctx, run); err != nil {
			return err
		}

		jobID, dispatchErr := dispatch(step)
		stepRun.CompletedAt = e.now().UTC()
		if dispatchErr != nil {
			stepRun.State = StateFailed
			stepRun.Error = dispatchErr.Error()
			if step.ContinueOnError {
				run.CurrentStep++
				if err := e.persist(ctx, run); err != nil {
					return err
				}
				continue
			}
			run.State = StateFailed
			if err := e.persist(ctx, run); err != nil {
				return err
			}
			return nil
		}

		stepRun.JobID = jobID
		stepRun.State = StateSucceeded
		run.CurrentStep++
		if err := e.persist(ctx, run); err != nil {
			return err
		}
	}

	run.State = StateSucceeded
	return e.persist(ctx, run)
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
