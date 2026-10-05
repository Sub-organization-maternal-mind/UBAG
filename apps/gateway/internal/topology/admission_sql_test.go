package topology

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func sqliteBackend(t *testing.T) (*SQLTokenBackend, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "admission.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	b := NewSQLiteTokenBackend(db)
	if err := b.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	return b, db
}

// postgresBackend returns a Postgres-backed store with its tables cleared, or
// skips (the repo's env-gated convention).
func postgresBackend(t *testing.T) *SQLTokenBackend {
	t.Helper()
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b := NewPostgresTokenBackend(db)
	if err := b.Ready(t.Context()); err != nil {
		t.Fatalf("postgres admission schema: %v", err)
	}
	for _, table := range []string{"gateway_admission_token_lanes", "gateway_admission_tokens", "gateway_admission_caps"} {
		if _, err := db.ExecContext(t.Context(), "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func eachBackend(t *testing.T, fn func(t *testing.T, b *SQLTokenBackend)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { b, _ := sqliteBackend(t); fn(t, b) })
	t.Run("postgres", func(t *testing.T) { fn(t, postgresBackend(t)) })
}

func TestAdmissionLaneCeilingIsAtomicUnderConcurrency(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *SQLTokenBackend) {
		const cap, clients = 5, 40
		var admitted atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < clients; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, ok, err := b.AcquireToken(t.Context(), []Lane{{Key: "lane:t|x", Cap: cap}}, time.Minute, time.Now().UTC())
				if err == nil && ok {
					admitted.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := admitted.Load(); got != cap {
			t.Fatalf("admitted %d, want exactly the ceiling %d", got, cap)
		}
	})
}

// Two registries on one database are two replicas: together they must not
// exceed the ceiling, and a job's token released by either frees the lane.
func TestRegistryReplicasShareOneCeiling(t *testing.T) {
	b, _ := sqliteBackend(t)
	a, c := NewConcurrencyRegistry(), NewConcurrencyRegistry()
	a.UseBackend(b, LaneLimits{})
	c.UseBackend(b, LaneLimits{})
	a.Report("t", ConcurrencyView{Target: "x", IdentityRef: "app", CurrentCap: 2})

	if !a.Acquire("t", "x", "app") || !c.Acquire("t", "x", "app") {
		t.Fatal("two admissions under a ceiling of 2 must succeed")
	}
	if a.Acquire("t", "x", "app") || c.Acquire("t", "x", "app") {
		t.Fatal("a third admission on either replica must be refused (shared ceiling)")
	}
	a.MarkAcquired("job-1", "t", "x", "app")
	c.ReleaseForJob("job-1") // terminal path on the OTHER replica
	if !c.Acquire("t", "x", "app") {
		t.Fatal("releasing job-1 on any replica must free its lane")
	}
}

func TestAdmissionMultiLaneIsAllOrNothing(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *SQLTokenBackend) {
		now := time.Now().UTC()
		lanes := []Lane{{Key: "lane:t|x", Cap: 10}, {Key: "tenant:t", Cap: 1}}
		if _, ok, err := b.AcquireToken(t.Context(), lanes, time.Minute, now); err != nil || !ok {
			t.Fatalf("first: ok=%v err=%v", ok, err)
		}
		if _, ok, _ := b.AcquireToken(t.Context(), lanes, time.Minute, now); ok {
			t.Fatal("tenant lane is full; admission must be refused")
		}
		counts, err := b.LaneKindCounts(t.Context(), now)
		if err != nil || counts["lane"] != 1 || counts["tenant"] != 1 {
			t.Fatalf("a refused admission must leave no partial lane rows: %v err=%v", counts, err)
		}
	})
}

func TestAdmissionUnassociatedTokensExpireAssociatedDoNot(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *SQLTokenBackend) {
		now := time.Now().UTC()
		lane := []Lane{{Key: "lane:t|x", Cap: 2}}
		orphan, _, _ := b.AcquireToken(t.Context(), lane, time.Minute, now)
		held, _, _ := b.AcquireToken(t.Context(), lane, time.Minute, now)
		_ = orphan
		if err := b.AssociateToken(t.Context(), held, "job-held"); err != nil {
			t.Fatal(err)
		}
		later := now.Add(2 * time.Minute)
		if _, ok, _ := b.AcquireToken(t.Context(), lane, time.Minute, later); !ok {
			t.Fatal("the expired unassociated token must stop counting")
		}
		if n, err := b.SweepExpired(t.Context(), later); err != nil || n != 1 {
			t.Fatalf("swept %d err=%v, want the single orphan", n, err)
		}
		if err := b.ReleaseJobToken(t.Context(), "job-held"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAdmissionDynamicCapComesFromSharedTable(t *testing.T) {
	eachBackend(t, func(t *testing.T, b *SQLTokenBackend) {
		now := time.Now().UTC()
		if err := b.PutLaneCap(context.Background(), "lane:t|x", 1, now); err != nil {
			t.Fatal(err)
		}
		lane := []Lane{{Key: "lane:t|x", Cap: 100, Dynamic: true}}
		if _, ok, _ := b.AcquireToken(t.Context(), lane, time.Minute, now); !ok {
			t.Fatal("first admission")
		}
		if _, ok, _ := b.AcquireToken(t.Context(), lane, time.Minute, now); ok {
			t.Fatal("the stored AIMD cap of 1 must govern, not the fallback of 100")
		}
	})
}
