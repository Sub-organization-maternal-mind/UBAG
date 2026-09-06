package jobs

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/sqlitestore"
	_ "modernc.org/sqlite"
)

// TestCreateScheduledPersistsNotBefore pins the contract's not_before option
// on the always-available backends: a future timestamp creates a scheduled
// job whose NotBefore survives a store round-trip; a past timestamp (or none)
// executes immediately as queued.
func TestCreateScheduledPersistsNotBefore(t *testing.T) {
	stores := map[string]Store{
		"memory": NewMemoryStore(),
		"sqlite": newScheduledTestSQLiteStore(t),
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			future := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)

			scheduled, err := store.Create(ctx, CreateRequest{
				APIVersion:  "2026-05-22",
				TenantID:    "tenant_sched",
				AppID:       "app_sched",
				Target:      "mock",
				CommandType: "submit",
				Input:       map[string]any{},
				NotBefore:   &future,
			})
			if err != nil {
				t.Fatalf("create scheduled: %v", err)
			}
			if scheduled.Status != StatusScheduled {
				t.Fatalf("status = %s, want %s", scheduled.Status, StatusScheduled)
			}
			loaded, found, err := store.Get(ctx, scheduled.ID)
			if err != nil || !found {
				t.Fatalf("get scheduled: found=%v err=%v", found, err)
			}
			if loaded.Status != StatusScheduled {
				t.Fatalf("reloaded status = %s, want %s", loaded.Status, StatusScheduled)
			}
			if loaded.NotBefore == nil || !loaded.NotBefore.Equal(future) {
				t.Fatalf("reloaded NotBefore = %v, want %v", loaded.NotBefore, future)
			}

			past := time.Now().UTC().Add(-time.Hour)
			immediate, err := store.Create(ctx, CreateRequest{
				APIVersion:  "2026-05-22",
				TenantID:    "tenant_sched",
				AppID:       "app_sched",
				Target:      "mock",
				CommandType: "submit",
				Input:       map[string]any{},
				NotBefore:   &past,
			})
			if err != nil {
				t.Fatalf("create past-due: %v", err)
			}
			if immediate.Status != StatusQueued {
				t.Fatalf("past-due status = %s, want %s", immediate.Status, StatusQueued)
			}
		})
	}
}

func newScheduledTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	// The real boot path: embedded schema first, then the store.
	if err := sqlitestore.Apply(context.Background(), db); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store := NewSQLiteStore(db)
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("ready: %v", err)
	}
	return store
}
