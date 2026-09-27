package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These tests mirror sqlite_cas_test.go for the Postgres backend, guarding the
// same exactly-once primitive (the attachment dispatch gate) against a lost
// update overwriting a terminal status. They run only when
// UBAG_TEST_POSTGRES_DSN is set; otherwise they skip, like every other
// Postgres store test in this package.

func TestPostgresTransitionStatusHasSingleConcurrentWinner(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}

	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresGatewayMigration(t, db)

	store := NewPostgresStore(db)
	job, err := store.Create(context.Background(), CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_pg_cas", AppID: "app_pg_cas",
		Target: "chatgpt_web", CommandType: "chat.prompt", Input: map[string]any{},
		AwaitingAttachments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPostgresJobs(t, db, "tenant_pg_cas")

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	errs := make([]error, 0, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, changed, err := store.TransitionStatus(context.Background(), job.ID, StatusCreated, StatusQueued)
			mu.Lock()
			defer mu.Unlock()
			// The loser must either report no change (status already advanced
			// when the FOR UPDATE read landed) or the typed ErrConflict from
			// the guarded UPDATE; it must never fail with a store error.
			if err != nil && !errors.Is(err, ErrConflict) {
				errs = append(errs, err)
			}
			if changed {
				winners++
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("transition errors: %v", errs)
	}
	if winners != 1 {
		t.Fatalf("CAS winners = %d, want exactly 1", winners)
	}
}

// TestPostgresTransitionStatusRefusesTerminalOverwrite pins the guard against
// the lost-update the unguarded UPDATE allowed: a caller that read a non-terminal
// status before another caller drove the job terminal must not clobber the
// terminal status back to a transient one.
func TestPostgresTransitionStatusRefusesTerminalOverwrite(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}

	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresGatewayMigration(t, db)

	store := NewPostgresStore(db)
	job, err := store.Create(context.Background(), CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_pg_cas", AppID: "app_pg_cas",
		Target: "chatgpt_web", CommandType: "chat.prompt", Input: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPostgresJobs(t, db, "tenant_pg_cas")

	if _, _, err := store.TransitionStatus(context.Background(), job.ID, StatusQueued, StatusRunning); err != nil {
		t.Fatalf("queue to running: %v", err)
	}
	if _, _, err := store.TransitionStatus(context.Background(), job.ID, StatusRunning, StatusCompleted); err != nil {
		t.Fatalf("running to completed: %v", err)
	}

	// A stale caller still believes the job is running and tries to move it to
	// completing; the CAS must refuse and surface the conflict.
	_, changed, err := store.TransitionStatus(context.Background(), job.ID, StatusRunning, StatusCompleting)
	if changed {
		t.Fatal("terminal overwrite was applied; CAS guard missing")
	}
	if err != nil && !errors.Is(err, ErrConflict) {
		t.Fatalf("unexpected error: %v", err)
	}
	current, found, err := store.Get(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("reload job: found=%v err=%v", found, err)
	}
	if current.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed (terminal status was overwritten)", current.Status)
	}
}

func TestPostgresTransitionStatusUsesConditionalUpdate(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("postgres.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "WHERE id = $4 AND status = $5") {
		t.Fatal("TransitionStatus must perform UPDATE ... WHERE id = $4 AND status = $5")
	}
}
