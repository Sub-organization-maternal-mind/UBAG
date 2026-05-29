package workflow

import (
	"context"
	"errors"
	"testing"
)

func sampleDefinition(tenant, app string) Definition {
	return Definition{
		TenantID: tenant,
		AppID:    app,
		Name:     "deploy pipeline",
		Steps: []Step{
			{ID: "build", Target: "mock", Command: "submit", Input: map[string]any{"prompt": "build"}},
			{ID: "test", Target: "mock", Command: "submit"},
			{ID: "ship", Target: "mock", Command: "submit"},
		},
	}
}

func okDispatch(jobID string) DispatchFunc {
	calls := 0
	return func(Step) (string, error) {
		calls++
		return jobID, nil
	}
}

func TestDefinitionCRUD(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	created, err := store.CreateDefinition(ctx, sampleDefinition("t1", "a1"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected generated definition id")
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("expected created_at set")
	}

	got, found, err := store.GetDefinition(ctx, "t1", "a1", created.ID)
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if len(got.Steps) != 3 || got.Steps[0].ID != "build" {
		t.Fatalf("unexpected steps: %+v", got.Steps)
	}

	// Scope isolation: another tenant cannot read it.
	if _, found, _ := store.GetDefinition(ctx, "t2", "a1", created.ID); found {
		t.Fatal("definition leaked across tenant scope")
	}

	list, err := store.ListDefinitions(ctx, "t1", "a1", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: len=%d err=%v", len(list), err)
	}
	if other, _ := store.ListDefinitions(ctx, "t2", "a1", 10); len(other) != 0 {
		t.Fatalf("expected empty list for other tenant, got %d", len(other))
	}
}

func TestDefinitionValidation(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	bad := sampleDefinition("t1", "a1")
	bad.Steps[1].Target = "BadTarget"
	if _, err := store.CreateDefinition(ctx, bad); !errors.Is(err, ErrInvalidDef) {
		t.Fatalf("expected ErrInvalidDef for bad target, got %v", err)
	}

	empty := sampleDefinition("t1", "a1")
	empty.Steps[0].Command = "   "
	if _, err := store.CreateDefinition(ctx, empty); !errors.Is(err, ErrInvalidDef) {
		t.Fatalf("expected ErrInvalidDef for empty command, got %v", err)
	}

	noScope := sampleDefinition("", "a1")
	if _, err := store.CreateDefinition(ctx, noScope); !errors.Is(err, ErrScope) {
		t.Fatalf("expected ErrScope, got %v", err)
	}
}

func TestAdvanceHappyPath(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	def, _ := store.CreateDefinition(ctx, sampleDefinition("t1", "a1"))

	run, err := store.CreateRun(ctx, Run{
		DefinitionID: def.ID,
		TenantID:     "t1",
		AppID:        "a1",
		Steps:        NewStepRuns(def),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	eng := NewEngine(store)
	if err := eng.Advance(ctx, &run, okDispatch("job-123")); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if run.State != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", run.State)
	}
	if run.CurrentStep != 3 {
		t.Fatalf("expected current step 3, got %d", run.CurrentStep)
	}
	for i, sr := range run.Steps {
		if sr.State != StateSucceeded {
			t.Fatalf("step %d state = %s", i, sr.State)
		}
		if sr.JobID != "job-123" {
			t.Fatalf("step %d job id = %q", i, sr.JobID)
		}
	}

	// Persisted run reflects the terminal state.
	persisted, found, _ := store.GetRun(ctx, "t1", "a1", run.ID)
	if !found || persisted.State != StateSucceeded {
		t.Fatalf("persisted run state = %s found=%v", persisted.State, found)
	}
}

func TestAdvanceFailureStopsRun(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	def, _ := store.CreateDefinition(ctx, sampleDefinition("t1", "a1"))
	run, _ := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1"})

	eng := NewEngine(store)
	dispatch := func(step Step) (string, error) {
		if step.ID == "test" {
			return "", errors.New("boom")
		}
		return "job-ok", nil
	}
	if err := eng.Advance(ctx, &run, dispatch); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if run.State != StateFailed {
		t.Fatalf("expected failed, got %s", run.State)
	}
	if run.CurrentStep != 1 {
		t.Fatalf("expected stop at step index 1, got %d", run.CurrentStep)
	}
	if run.Steps[0].State != StateSucceeded {
		t.Fatalf("step 0 should be succeeded, got %s", run.Steps[0].State)
	}
	if run.Steps[1].State != StateFailed || run.Steps[1].Error != "boom" {
		t.Fatalf("step 1 should be failed with error, got %+v", run.Steps[1])
	}
	if run.Steps[2].State != StatePending {
		t.Fatalf("step 2 should remain pending, got %s", run.Steps[2].State)
	}
}

func TestAdvanceContinueOnError(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	d := sampleDefinition("t1", "a1")
	d.Steps[1].ContinueOnError = true
	def, _ := store.CreateDefinition(ctx, d)
	run, _ := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1"})

	eng := NewEngine(store)
	dispatch := func(step Step) (string, error) {
		if step.ID == "test" {
			return "", errors.New("flaky")
		}
		return "job-ok", nil
	}
	if err := eng.Advance(ctx, &run, dispatch); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if run.State != StateSucceeded {
		t.Fatalf("expected succeeded despite continue-on-error, got %s", run.State)
	}
	if run.Steps[1].State != StateFailed {
		t.Fatalf("failed step should retain failed state, got %s", run.Steps[1].State)
	}
	if run.Steps[2].State != StateSucceeded {
		t.Fatalf("final step should have run, got %s", run.Steps[2].State)
	}
}

func TestCancel(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	def, _ := store.CreateDefinition(ctx, sampleDefinition("t1", "a1"))
	run, _ := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1", Steps: NewStepRuns(def)})

	eng := NewEngine(store)
	if err := eng.Cancel(ctx, &run); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if run.State != StateCanceled {
		t.Fatalf("expected canceled, got %s", run.State)
	}
	for i, sr := range run.Steps {
		if sr.State != StateCanceled {
			t.Fatalf("step %d expected canceled, got %s", i, sr.State)
		}
	}
	// Cannot advance a canceled run.
	if err := eng.Advance(ctx, &run, okDispatch("x")); !errors.Is(err, ErrTerminalRun) {
		t.Fatalf("expected ErrTerminalRun, got %v", err)
	}
}

func TestRunScopeIsolation(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	def, _ := store.CreateDefinition(ctx, sampleDefinition("t1", "a1"))
	run, _ := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1"})

	if _, found, _ := store.GetRun(ctx, "t2", "a1", run.ID); found {
		t.Fatal("run leaked across tenant")
	}
	if _, found, _ := store.GetRun(ctx, "t1", "a2", run.ID); found {
		t.Fatal("run leaked across app")
	}
	if list, _ := store.ListRuns(ctx, "t2", "a1", 10); len(list) != 0 {
		t.Fatalf("expected empty run list for other tenant, got %d", len(list))
	}
}

func TestRunIdempotencyKey(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	def, _ := store.CreateDefinition(ctx, sampleDefinition("t1", "a1"))

	first, err := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1", IdempotencyKey: "idem-1"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1", IdempotencyKey: "idem-1"})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotency key not carried: %s != %s", first.ID, second.ID)
	}
	if second.IdempotencyKey != "idem-1" {
		t.Fatalf("idempotency key not stored: %q", second.IdempotencyKey)
	}
	// Same key under a different tenant must produce a distinct run.
	def2, _ := store.CreateDefinition(ctx, sampleDefinition("t2", "a1"))
	other, _ := store.CreateRun(ctx, Run{DefinitionID: def2.ID, TenantID: "t2", AppID: "a1", IdempotencyKey: "idem-1"})
	if other.ID == first.ID {
		t.Fatal("idempotency key collided across tenant scope")
	}
}
