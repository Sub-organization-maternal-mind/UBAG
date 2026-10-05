package voice

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newMemoryStore(t *testing.T) *MemoryStore {
	t.Helper()
	return NewMemoryStore()
}

func newSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "voice.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewSQLiteStore(db)
	if err := store.Ready(t.Context()); err != nil {
		t.Fatalf("ready: %v", err)
	}
	return store
}

func reserveRequest(id string) ReserveRequest {
	return ReserveRequest{
		SessionID:          id,
		TenantID:           "tenant_a",
		AppID:              "app_a",
		Target:             "chatgpt_web",
		IdentityCandidates: []string{"acct-1", "acct-2"},
		InstanceCandidates: []string{"browser-1"},
		LeaseTTL:           5 * time.Minute,
		Now:                time.Now().UTC(),
	}
}

// TestVoiceStoreContract runs the shared semantics against one store.
func TestVoiceStoreContract(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testVoiceStoreContract(t, newMemoryStore(t)) })
	t.Run("sqlite", func(t *testing.T) { testVoiceStoreContract(t, newSQLiteStore(t)) })
}

type voiceStore interface {
	Store
	AsStore() Store
}

func testVoiceStoreContract(t *testing.T, store Store) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()

	// First reserve claims the only environment and the first free account.
	first, err := store.Reserve(ctx, reserveRequest("voice_1"))
	if err != nil {
		t.Fatalf("reserve first: %v", err)
	}
	if first.Status != StatusConnecting || first.IdentityRef != "acct-1" || first.InstanceRef != "browser-1" {
		t.Fatalf("first = %+v", first)
	}

	// A second session queues: the environment is exclusive.
	second, err := store.Reserve(ctx, reserveRequest("voice_2"))
	if err != nil {
		t.Fatalf("reserve second: %v", err)
	}
	if second.Status != StatusQueued || second.IdentityRef != "" || second.InstanceRef != "" {
		t.Fatalf("second must queue without leases, got %+v", second)
	}

	// A different tenant shares nothing: its own lease set.
	other := reserveRequest("voice_other")
	other.TenantID = "tenant_b"
	otherRes, err := store.Reserve(ctx, other)
	if err != nil {
		t.Fatalf("reserve other tenant: %v", err)
	}
	if otherRes.Status != StatusConnecting {
		t.Fatalf("other tenant must claim its own leases, got %+v", otherRes)
	}

	// Cross-tenant reads are isolated.
	if _, found, err := store.Get(ctx, "tenant_b", "voice_1"); err != nil || found {
		t.Fatalf("cross-tenant get must miss: found=%v err=%v", found, err)
	}

	// Terminating the first frees the environment; the queued session claims it.
	if err := store.Terminate(ctx, "tenant_a", "voice_1", now.Add(time.Second), "client_disconnect"); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	claimed, err := store.Claim(ctx, "tenant_a", "voice_2", []string{"acct-1", "acct-2"}, []string{"browser-1"}, time.Minute, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("claim after terminate: %v", err)
	}
	if claimed.Status != StatusConnecting || claimed.IdentityRef != "acct-1" || claimed.InstanceRef != "browser-1" {
		t.Fatalf("claimed = %+v", claimed)
	}

	// Claim is CAS: claiming again conflicts.
	if _, err := store.Claim(ctx, "tenant_a", "voice_2", []string{"acct-1"}, []string{"browser-1"}, time.Minute, now.Add(3*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-claim = %v, want ErrConflict", err)
	}

	// Connected transition works; mute only on live sessions.
	if err := store.Transition(ctx, "tenant_a", "voice_2", StatusConnecting, StatusConnected, now.Add(4*time.Second), ""); err != nil {
		t.Fatalf("transition to connected: %v", err)
	}
	if err := store.SetMuted(ctx, "tenant_a", "voice_2", true, now.Add(5*time.Second)); err != nil {
		t.Fatalf("mute: %v", err)
	}
	got, found, err := store.Get(ctx, "tenant_a", "voice_2")
	if err != nil || !found {
		t.Fatalf("get: %v %v", found, err)
	}
	if !got.Muted || got.Status != StatusConnected {
		t.Fatalf("got = %+v", got)
	}

	// Lease renewal extends the deadline.
	later := now.Add(10 * time.Minute)
	if err := store.RenewLease(ctx, "tenant_a", "voice_2", later, now.Add(6*time.Second)); err != nil {
		t.Fatalf("renew: %v", err)
	}
	got, _, _ = store.Get(ctx, "tenant_a", "voice_2")
	if got.LeaseExpires.Before(now.Add(9 * time.Minute)) {
		t.Fatalf("lease not renewed: %v", got.LeaseExpires)
	}

	// Sweeper terminates only lapsed ACTIVE sessions, never queued ones.
	if _, err := store.Reserve(ctx, reserveRequest("voice_3")); err != nil {
		t.Fatalf("reserve third (must queue): %v", err)
	}
	swept, err := store.SweepExpired(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(swept) != 0 {
		t.Fatalf("renewed lease must survive sweep, swept %v", swept)
	}
	if err := store.RenewLease(ctx, "tenant_a", "voice_2", now.Add(3*time.Minute), now.Add(7*time.Second)); err != nil {
		t.Fatalf("renew short: %v", err)
	}
	swept, err = store.SweepExpired(ctx, now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("sweep 2: %v", err)
	}
	if len(swept) != 1 || swept[0] != "voice_2" {
		t.Fatalf("sweep must catch the lapsed session, got %v", swept)
	}
	// The freed environment lets the queued session claim.
	if _, err := store.Claim(ctx, "tenant_a", "voice_3", []string{"acct-2"}, []string{"browser-1"}, time.Minute, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("claim after sweep: %v", err)
	}

	count, err := store.ActiveCount(ctx, "tenant_a", "")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("active count = %d, want 1", count)
	}
}

// TestVoiceReserveSkipsHalfReservations: an account can never be pinned
// without also claiming an environment.
func TestVoiceReserveSkipsHalfReservations(t *testing.T) {
	store := newMemoryStore(t)
	ctx := t.Context()

	req := reserveRequest("voice_full")
	req.IdentityCandidates = []string{"acct-1"}
	req.InstanceCandidates = []string{"browser-1"}
	if _, err := store.Reserve(ctx, req); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// No environments free at all → must queue even though accounts remain.
	req2 := reserveRequest("voice_queued")
	req2.IdentityCandidates = []string{"acct-2"}
	req2.InstanceCandidates = []string{}
	got, err := store.Reserve(ctx, req2)
	if err != nil {
		t.Fatalf("reserve queued: %v", err)
	}
	if got.Status != StatusQueued {
		t.Fatalf("status = %s, want queued", got.Status)
	}
}

// TestVoiceConcurrentReserveSingleWinner: N goroutines racing for one
// environment produce exactly one connecting session.
func TestVoiceConcurrentReserveSingleWinner(t *testing.T) {
	store := newMemoryStore(t)
	ctx := t.Context()
	var wg sync.WaitGroup
	var mu sync.Mutex
	connecting := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			req := reserveRequest("voice_race_" + string(rune('a'+n)))
			res, err := store.Reserve(ctx, req)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if res.Status == StatusConnecting {
				connecting++
			}
		}(i)
	}
	wg.Wait()
	if connecting != 1 {
		t.Fatalf("connecting sessions = %d, want exactly 1", connecting)
	}
}
