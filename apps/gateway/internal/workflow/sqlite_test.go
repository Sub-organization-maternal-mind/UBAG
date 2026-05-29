package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"database/sql"
)

func newSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "workflow.db") + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	store := NewSQLiteStore(db)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func TestSQLiteDefinitionAndRunLifecycle(t *testing.T) {
	store := newSQLiteStore(t)
	ctx := context.Background()

	def, err := store.CreateDefinition(ctx, sampleDefinition("t1", "a1"))
	if err != nil {
		t.Fatalf("create definition: %v", err)
	}
	if def.ID == "" || def.CreatedAt.IsZero() {
		t.Fatalf("definition not initialized: %+v", def)
	}

	got, found, err := store.GetDefinition(ctx, "t1", "a1", def.ID)
	if err != nil || !found {
		t.Fatalf("get definition: found=%v err=%v", found, err)
	}
	if len(got.Steps) != 3 || got.Steps[2].ID != "ship" {
		t.Fatalf("steps not persisted: %+v", got.Steps)
	}

	if _, found, _ := store.GetDefinition(ctx, "t2", "a1", def.ID); found {
		t.Fatal("definition leaked across scope")
	}

	list, err := store.ListDefinitions(ctx, "t1", "a1", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list definitions: len=%d err=%v", len(list), err)
	}

	run, err := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1", Steps: NewStepRuns(def)})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.State != StatePending {
		t.Fatalf("expected pending run, got %s", run.State)
	}

	eng := NewEngine(store)
	if err := eng.Advance(ctx, &run, okDispatch("job-sqlite")); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if run.State != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", run.State)
	}

	persisted, found, err := store.GetRun(ctx, "t1", "a1", run.ID)
	if err != nil || !found {
		t.Fatalf("get run: found=%v err=%v", found, err)
	}
	if persisted.State != StateSucceeded || persisted.CurrentStep != 3 {
		t.Fatalf("persisted run wrong: state=%s step=%d", persisted.State, persisted.CurrentStep)
	}
	for i, sr := range persisted.Steps {
		if sr.State != StateSucceeded || sr.JobID != "job-sqlite" {
			t.Fatalf("persisted step %d wrong: %+v", i, sr)
		}
	}

	runs, err := store.ListRuns(ctx, "t1", "a1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("list runs: len=%d err=%v", len(runs), err)
	}
	if list, _ := store.ListRuns(ctx, "t2", "a1", 10); len(list) != 0 {
		t.Fatalf("run list leaked across scope: %d", len(list))
	}
}

func TestSQLiteRunIdempotency(t *testing.T) {
	store := newSQLiteStore(t)
	ctx := context.Background()
	def, _ := store.CreateDefinition(ctx, sampleDefinition("t1", "a1"))

	first, err := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1", IdempotencyKey: "idem-sql"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1", IdempotencyKey: "idem-sql"})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotency not honored: %s != %s", first.ID, second.ID)
	}
}

func TestSQLiteUpdateRunScope(t *testing.T) {
	store := newSQLiteStore(t)
	ctx := context.Background()
	def, _ := store.CreateDefinition(ctx, sampleDefinition("t1", "a1"))
	run, _ := store.CreateRun(ctx, Run{DefinitionID: def.ID, TenantID: "t1", AppID: "a1"})

	run.TenantID = "t2"
	if err := store.UpdateRun(ctx, run); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound updating out-of-scope run, got %v", err)
	}
}
