package voice

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// postgresVoiceStore returns a PostgresStore over the real migration chain
// with the table cleared, or skips (the repo's env-gated convention).
func postgresVoiceStore(t *testing.T) (*PostgresStore, *sql.DB) {
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
	store := NewPostgresStore(db)
	if err := store.Ready(t.Context()); err != nil {
		t.Fatalf("voice schema (apply migrations 0019 and 0020): %v", err)
	}
	if _, err := db.ExecContext(t.Context(), "DELETE FROM gateway_voice_sessions"); err != nil {
		t.Fatal(err)
	}
	return store, db
}

// The same contract the memory and SQLite stores satisfy.
func TestVoicePostgresStoreContract(t *testing.T) {
	store, _ := postgresVoiceStore(t)
	testVoiceStoreContract(t, store)
}

func TestVoicePostgresExclusivityBudgetsAndRenewal(t *testing.T) {
	store, _ := postgresVoiceStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	// A physical environment is exclusive across tenants.
	req := reserveRequest("a")
	req.Placements = []Placement{{"account", "physical-browser"}}
	if a, err := store.Reserve(ctx, req); err != nil || a.Status != StatusConnecting {
		t.Fatalf("a = %+v err=%v", a, err)
	}
	req.SessionID, req.TenantID = "b", "tenant_b"
	if b, err := store.Reserve(ctx, req); err != nil || b.Status != StatusQueued {
		t.Fatalf("second tenant on the same environment must queue: %+v err=%v", b, err)
	}

	// An expired lease cannot be renewed before the sweeper runs.
	exp := reserveRequest("exp")
	exp.Placements, exp.LeaseTTL, exp.Now = []Placement{{"acct-exp", "browser-exp"}}, time.Minute, now
	if _, err := store.Reserve(ctx, exp); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLease(ctx, "tenant_a", "exp", now.Add(3*time.Minute), now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired lease revived")
	}

	// Active/queue budgets inside admission.
	two := []Placement{{"acct-1", "browser-1"}, {"acct-2", "browser-2"}}
	mk := func(id string) ReserveRequest {
		r := reserveRequest(id)
		r.TenantID = "tenant_budget"
		r.Placements, r.MaxActive, r.MaxQueued = two, 1, 1
		return r
	}
	if first, err := store.Reserve(ctx, mk("c1")); err != nil || first.Status != StatusConnecting {
		t.Fatalf("c1 = %+v err=%v", first, err)
	}
	if second, err := store.Reserve(ctx, mk("c2")); err != nil || second.Status != StatusQueued {
		t.Fatalf("cap=1 must queue c2: %+v err=%v", second, err)
	}
	if _, err := store.Reserve(ctx, mk("c3")); err != ErrQueueFull {
		t.Fatalf("c3 = %v, want ErrQueueFull", err)
	}
	leased, queued, err := store.TenantCounts(ctx, "tenant_budget")
	if err != nil || leased != 1 || queued != 1 {
		t.Fatalf("counts = %d/%d err=%v", leased, queued, err)
	}
}

// Concurrent admissions across "replicas" (independent connections) never
// exceed the tenant's active budget or double-lease an environment.
func TestVoicePostgresConcurrentAdmissionHonorsBudgetAndExclusivity(t *testing.T) {
	_, db := postgresVoiceStore(t)
	const clients, environments, maxActive = 24, 12, 3
	var placements []Placement
	for i := 0; i < environments; i++ {
		placements = append(placements, Placement{fmt.Sprintf("acct-%d", i), fmt.Sprintf("browser-%d", i)})
	}
	var leased atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// A separate store handle per client mimics separate replicas.
			s := NewPostgresStore(db)
			r := reserveRequest(fmt.Sprintf("race-%d", n))
			r.TenantID = "tenant_race"
			r.Placements, r.MaxActive, r.MaxQueued = placements, maxActive, 100
			got, err := s.Reserve(t.Context(), r)
			if err == nil && got.Status == StatusConnecting {
				leased.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := leased.Load(); got != maxActive {
		t.Fatalf("leased %d sessions, want exactly the active budget %d", got, maxActive)
	}
	var distinct int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(DISTINCT instance_ref) FROM gateway_voice_sessions
WHERE tenant_id = 'tenant_race' AND status IN ('connecting','connected') AND instance_ref <> ''`).Scan(&distinct); err != nil || distinct != maxActive {
		t.Fatalf("distinct leased environments = %d err=%v, want %d (no double lease)", distinct, err, maxActive)
	}
}

// Sweep and renew racing on the same session: the outcome is always
// consistent (either swept, or renewed and alive), never revived or half-swept.
func TestVoicePostgresSweepRenewRace(t *testing.T) {
	store, _ := postgresVoiceStore(t)
	for i := 0; i < 20; i++ {
		now := time.Now().UTC()
		id := fmt.Sprintf("sweep-%d", i)
		r := reserveRequest(id)
		r.TenantID = "tenant_sweep"
		r.Placements = []Placement{{fmt.Sprintf("acct-s%d", i), fmt.Sprintf("browser-s%d", i)}}
		r.LeaseTTL, r.Now = time.Second, now
		if _, err := store.Reserve(t.Context(), r); err != nil {
			t.Fatal(err)
		}
		later := now.Add(2 * time.Second) // the lease has lapsed by then
		var wg sync.WaitGroup
		wg.Add(2)
		var renewErr error
		go func() { defer wg.Done(); _, _ = store.SweepExpired(t.Context(), later) }()
		go func() {
			defer wg.Done()
			renewErr = store.RenewLease(t.Context(), "tenant_sweep", id, later.Add(time.Minute), later)
		}()
		wg.Wait()
		got, _, err := store.Get(t.Context(), "tenant_sweep", id)
		if err != nil {
			t.Fatal(err)
		}
		if renewErr == nil {
			t.Fatalf("%s: a lapsed lease was renewed", id)
		}
		if got.Status != StatusTerminated && got.Status != StatusConnecting {
			t.Fatalf("%s: unexpected status %s", id, got.Status)
		}
	}
}
