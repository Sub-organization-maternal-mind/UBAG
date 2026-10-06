package voice

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// P5.8 store contract: node-bound lease generation fencing, the node's media
// lease versus the sweeper, and the terminating hold. Every function takes a
// Store so the memory, SQLite and (env-gated) Postgres stores run the same
// assertions; all times are explicit so no assertion depends on a clock.

var leaseNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// leaseContract is the shared table; postgres_test.go runs it against the real
// schema, one fresh table per entry.
var leaseContract = []struct {
	name string
	run  func(*testing.T, Store)
}{
	{"StaleGenerationCommitRejected", testLeaseStaleGenerationRejected},
	{"MediaLeaseRenewVersusSweep", testMediaLeaseRenewVersusSweep},
	{"TerminatingHoldBlocksReuse", testTerminatingHoldBlocksReuse},
	{"TerminatingHoldIsFenced", testTerminatingHoldIsFenced},
	{"LeaseHoldersFollowLeases", testLeaseHoldersFollowLeases},
	{"PlacementLeaseHoldersCarryTheNode", testPlacementLeaseHoldersCarryTheNode},
}

func TestVoiceLeaseContract(t *testing.T) {
	for _, c := range leaseContract {
		t.Run(c.name, func(t *testing.T) { eachStore(t, c.run) })
	}
}

func reserveAt(t *testing.T, st Store, id, tenant string, now time.Time, ttl time.Duration, placements ...Placement) Session {
	t.Helper()
	req := reserveRequest(id)
	req.TenantID, req.Placements, req.LeaseTTL, req.Now = tenant, placements, ttl, now
	got, err := st.Reserve(t.Context(), req)
	if err != nil {
		t.Fatalf("reserve %s: %v", id, err)
	}
	return got
}

func mustGet(t *testing.T, st Store, tenant, id string) Session {
	t.Helper()
	got, found, err := st.Get(t.Context(), tenant, id)
	if err != nil || !found {
		t.Fatalf("get %s: found=%v err=%v", id, found, err)
	}
	return got
}

// closeTo tolerates the microsecond rounding of a SQL round trip.
func closeTo(a, b time.Time) bool {
	d := a.Sub(b)
	return d < time.Millisecond && d > -time.Millisecond
}

func claimAt(t *testing.T, st Store, id string, now time.Time, p Placement) (Session, error) {
	t.Helper()
	return st.Claim(t.Context(), claimReq(id, now, p))
}

// A replaced node (or a forged generation) can neither commit nor renew.
func testLeaseStaleGenerationRejected(t *testing.T, st Store) {
	ctx := t.Context()
	s := reserveAt(t, st, "f1", "tenant_a", leaseNow, time.Hour, Placement{"acct-1", "browser-1"})
	if s.Status != StatusConnecting || s.NodeID != "" || s.LeaseGeneration != 0 {
		t.Fatalf("a primary-hosted session carries no node: %+v", s)
	}

	a, err := st.BindNode(ctx, "tenant_a", "f1", "node-a", 30*time.Second, leaseNow)
	if err != nil || a.NodeID != "node-a" || a.LeaseGeneration != 1 || !closeTo(a.MediaLeaseExpires, leaseNow.Add(30*time.Second)) {
		t.Fatalf("first bind = %+v err=%v", a, err)
	}
	b, err := st.BindNode(ctx, "tenant_a", "f1", "node-b", 30*time.Second, leaseNow.Add(time.Second))
	if err != nil || b.NodeID != "node-b" || b.LeaseGeneration != 2 {
		t.Fatalf("rebind = %+v err=%v (the generation must grow on every bind)", b, err)
	}

	later := leaseNow.Add(2 * time.Second)
	for _, c := range []struct {
		name  string
		fence LeaseFence
		want  error
	}{
		{"replaced node, its old generation", LeaseFence{"node-a", 1}, ErrNodeMismatch},
		{"replaced node, the current generation", LeaseFence{"node-a", 2}, ErrNodeMismatch},
		{"current node, an older generation", LeaseFence{"node-b", 1}, ErrStaleGeneration},
		{"current node, a forged future generation", LeaseFence{"node-b", 3}, ErrStaleGeneration},
		{"no fence at all", LeaseFence{}, ErrBadBinding},
	} {
		if err := st.CommitTransition(ctx, "tenant_a", "f1", c.fence, StatusConnecting, StatusConnected, later, ""); !errors.Is(err, c.want) {
			t.Errorf("%s: commit = %v, want %v", c.name, err, c.want)
		}
		if err := st.RenewMediaLease(ctx, "tenant_a", "f1", c.fence, leaseNow.Add(time.Hour), later); !errors.Is(err, c.want) {
			t.Errorf("%s: renew = %v, want %v", c.name, err, c.want)
		}
	}
	got := mustGet(t, st, "tenant_a", "f1")
	if got.Status != StatusConnecting || !closeTo(got.MediaLeaseExpires, leaseNow.Add(31*time.Second)) {
		t.Fatalf("a rejected write changed the session: %+v", got)
	}

	// The current holder commits and renews; the CAS on status still applies.
	current := LeaseFence{"node-b", 2}
	if err := st.CommitTransition(ctx, "tenant_a", "f1", current, StatusConnecting, StatusConnected, later, ""); err != nil {
		t.Fatalf("current holder commit: %v", err)
	}
	if err := st.CommitTransition(ctx, "tenant_a", "f1", current, StatusConnecting, StatusConnected, later, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeated commit = %v, want ErrConflict", err)
	}
	if err := st.RenewMediaLease(ctx, "tenant_a", "f1", current, leaseNow.Add(time.Hour), later); err != nil {
		t.Fatalf("current holder renew: %v", err)
	}
	if got := mustGet(t, st, "tenant_a", "f1"); got.Status != StatusConnected || !closeTo(got.MediaLeaseExpires, leaseNow.Add(time.Hour)) {
		t.Fatalf("after commit and renew: %+v", got)
	}

	// Tenant scope, unknown sessions and unbindable sessions.
	if err := st.CommitTransition(ctx, "tenant_b", "f1", current, StatusConnected, StatusTerminated, later, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant commit = %v, want ErrNotFound", err)
	}
	if err := st.RenewMediaLease(ctx, "tenant_a", "nope", current, later, later); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session renew = %v, want ErrNotFound", err)
	}
	if _, err := st.BindNode(ctx, "tenant_b", "f1", "node-b", time.Minute, later); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant bind = %v, want ErrNotFound", err)
	}
	for _, bad := range []string{"", "a|b"} {
		if _, err := st.BindNode(ctx, "tenant_a", "f1", bad, time.Minute, later); !errors.Is(err, ErrBadBinding) {
			t.Errorf("bind node %q = %v, want ErrBadBinding", bad, err)
		}
	}
	if _, err := st.BindNode(ctx, "tenant_a", "f1", "node-b", 0, later); !errors.Is(err, ErrBadBinding) {
		t.Errorf("bind without a media ttl = %v, want ErrBadBinding", err)
	}
	queued := reserveAt(t, st, "f2", "tenant_a", leaseNow, time.Hour, Placement{"acct-1", "browser-1"})
	if queued.Status != StatusQueued {
		t.Fatalf("f2 should queue behind f1: %+v", queued)
	}
	if _, err := st.BindNode(ctx, "tenant_a", "f2", "node-b", time.Minute, later); !errors.Is(err, ErrConflict) {
		t.Errorf("bind of a queued session = %v, want ErrConflict", err)
	}
	short := reserveAt(t, st, "f3", "tenant_a", leaseNow, time.Minute, Placement{"acct-3", "browser-3"})
	if _, err := st.BindNode(ctx, "tenant_a", short.ID, "node-b", time.Minute, leaseNow.Add(2*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Errorf("bind after the lease lapsed = %v, want ErrConflict", err)
	}

	// A terminated session accepts no more node writes, even the holder's.
	if err := st.Terminate(ctx, "tenant_a", "f1", later, "done"); err != nil {
		t.Fatal(err)
	}
	if err := st.RenewMediaLease(ctx, "tenant_a", "f1", current, leaseNow.Add(2*time.Hour), later); !errors.Is(err, ErrConflict) {
		t.Errorf("renew after terminate = %v, want ErrConflict", err)
	}
	if err := st.CommitTransition(ctx, "tenant_a", "f1", current, StatusConnected, StatusConnected, later, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("commit after terminate = %v, want ErrConflict", err)
	}
}

// The node's media lease is renewed by that node alone and swept like the
// client lease: renew and sweep agree on the instant, a lapsed lease is never
// revived, and the reason says which lease lapsed.
func testMediaLeaseRenewVersusSweep(t *testing.T, st Store) {
	ctx := t.Context()
	reserveAt(t, st, "m1", "tenant_a", leaseNow, time.Hour, Placement{"acct-1", "browser-1"})
	bound, err := st.BindNode(ctx, "tenant_a", "m1", "node-a", 30*time.Second, leaseNow)
	if err != nil {
		t.Fatal(err)
	}
	fence := LeaseFence{bound.NodeID, bound.LeaseGeneration}
	sweepIDs := func(now time.Time) []string {
		t.Helper()
		ids, err := st.SweepExpired(ctx, now)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		return ids
	}

	if ids := sweepIDs(leaseNow.Add(10 * time.Second)); len(ids) != 0 {
		t.Fatalf("a live media lease was swept: %v", ids)
	}
	if err := st.RenewMediaLease(ctx, "tenant_a", "m1", fence, leaseNow.Add(80*time.Second), leaseNow.Add(20*time.Second)); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if ids := sweepIDs(leaseNow.Add(60 * time.Second)); len(ids) != 0 {
		t.Fatalf("a renewed media lease was swept at +60s: %v", ids)
	}
	// Past the renewed deadline the lease cannot be revived; only the sweep acts.
	if err := st.RenewMediaLease(ctx, "tenant_a", "m1", fence, leaseNow.Add(3*time.Hour), leaseNow.Add(90*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("lapsed media lease renewed: %v", err)
	}
	if ids := sweepIDs(leaseNow.Add(90 * time.Second)); len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("sweep = %v, want [m1]", ids)
	}
	got := mustGet(t, st, "tenant_a", "m1")
	if got.Status != StatusTerminated || got.LastError != "media_lease_expired" || !got.LeaseExpires.After(leaseNow.Add(90*time.Second)) {
		t.Fatalf("swept session = %+v (the client lease was still valid)", got)
	}
	// The sweep freed the placement.
	if again := reserveAt(t, st, "m2", "tenant_a", leaseNow.Add(91*time.Second), time.Hour, Placement{"acct-1", "browser-1"}); again.Status != StatusConnecting {
		t.Fatalf("placement not freed by the media sweep: %+v", again)
	}

	// A session no node ever bound is not swept for a media lease it never had.
	reserveAt(t, st, "m3", "tenant_a", leaseNow, time.Hour, Placement{"acct-3", "browser-3"})
	// When both leases lapsed the client lease is the reported reason.
	reserveAt(t, st, "m4", "tenant_a", leaseNow, time.Minute, Placement{"acct-4", "browser-4"})
	if _, err := st.BindNode(ctx, "tenant_a", "m4", "node-a", 30*time.Second, leaseNow); err != nil {
		t.Fatal(err)
	}
	if ids := sweepIDs(leaseNow.Add(2 * time.Minute)); len(ids) != 1 || ids[0] != "m4" {
		t.Fatalf("sweep = %v, want [m4]", ids)
	}
	if got := mustGet(t, st, "tenant_a", "m4"); got.LastError != "lease_expired" {
		t.Fatalf("m4 last_error = %q, want lease_expired", got.LastError)
	}
	if got := mustGet(t, st, "tenant_a", "m3"); got.Status != StatusConnecting {
		t.Fatalf("unbound m3 = %s, want connecting", got.Status)
	}
}

// BeginTerminate reads as terminated at once but keeps the account and the
// environment reserved until the deactivate ack or the 45 s cap.
func testTerminatingHoldBlocksReuse(t *testing.T, st Store) {
	ctx := t.Context()
	pair := Placement{"acct-1", "browser-1"}
	reserveAt(t, st, "h-a", "tenant_a", leaseNow, time.Hour, pair)
	now := leaseNow.Add(time.Second)
	if err := st.BeginTerminate(ctx, "tenant_a", "h-a", now, "terminated_by_client", TerminatingHoldMax); err != nil {
		t.Fatalf("begin terminate: %v", err)
	}
	got := mustGet(t, st, "tenant_a", "h-a")
	if got.Status != StatusTerminated || got.LastError != "terminated_by_client" || got.IdentityRef != "" || got.InstanceRef != "" ||
		!closeTo(got.TerminatingUntil, now.Add(TerminatingHoldMax)) {
		t.Fatalf("held session = %+v (must read terminated, report no leases, hold for 45 s)", got)
	}

	// Every successor is blocked: the same account, the same environment, and
	// the same environment for another tenant.
	for i, c := range []struct {
		tenant string
		p      Placement
	}{
		{"tenant_a", Placement{"acct-1", "browser-2"}},
		{"tenant_a", Placement{"acct-9", "browser-1"}},
		{"tenant_b", Placement{"acct-1", "browser-1"}},
	} {
		id := fmt.Sprintf("h-blocked-%d", i)
		if s := reserveAt(t, st, id, c.tenant, now.Add(time.Second), time.Hour, c.p); s.Status != StatusQueued {
			t.Fatalf("%s took a held lease: %+v", id, s)
		}
	}
	if _, err := claimAt(t, st, "h-blocked-0", now.Add(2*time.Second), Placement{"acct-1", "browser-2"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim of a held account = %v, want ErrConflict", err)
	}
	if free := reserveAt(t, st, "h-free", "tenant_a", now, time.Hour, Placement{"acct-5", "browser-5"}); free.Status != StatusConnecting {
		t.Fatalf("an unrelated placement must stay free: %+v", free)
	}
	// Held leases are not the tenant's live sessions: the budget ignores them.
	if leased, _, err := st.TenantCounts(ctx, "tenant_a"); err != nil || leased != 1 {
		t.Fatalf("leased = %d err=%v, want only h-free", leased, err)
	}

	// A stand-in for a late plain Terminate keeps the hold; a wrong fence
	// cannot ack; the deactivate ack frees the leases at once and is idempotent.
	if err := st.Terminate(ctx, "tenant_a", "h-a", now, "late"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseHold(ctx, "tenant_a", "h-a", LeaseFence{"node-x", 1}, now); !errors.Is(err, ErrNodeMismatch) {
		t.Fatalf("ack with a foreign fence = %v, want ErrNodeMismatch", err)
	}
	if _, err := claimAt(t, st, "h-blocked-0", now.Add(3*time.Second), Placement{"acct-1", "browser-2"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the hold did not survive Terminate or a bad ack: %v", err)
	}
	if err := st.ReleaseHold(ctx, "tenant_a", "h-a", LeaseFence{}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	claimed, err := claimAt(t, st, "h-blocked-0", now.Add(5*time.Second), Placement{"acct-1", "browser-2"})
	if err != nil || claimed.Status != StatusConnecting {
		t.Fatalf("claim after the ack = %+v err=%v", claimed, err)
	}
	if err := st.ReleaseHold(ctx, "tenant_a", "h-a", LeaseFence{}, now.Add(6*time.Second)); err != nil {
		t.Fatalf("repeated ack = %v, want nil", err)
	}
	if got := mustGet(t, st, "tenant_a", "h-a"); !got.TerminatingUntil.IsZero() {
		t.Fatalf("hold not cleared: %v", got.TerminatingUntil)
	}
	if err := st.ReleaseHold(ctx, "tenant_a", "h-blocked-0", LeaseFence{}, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("ack of a LIVE session = %v, want ErrConflict (its leases are never released here)", err)
	}
	if err := st.ReleaseHold(ctx, "tenant_b", "h-a", LeaseFence{}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant ack = %v, want ErrNotFound", err)
	}

	// No ack ever arrives: the hold is capped at 45 s whatever was asked for,
	// and elapses without the sweeper.
	second := Placement{"acct-2", "browser-8"}
	reserveAt(t, st, "h-b", "tenant_a", leaseNow, time.Hour, second)
	if err := st.BeginTerminate(ctx, "tenant_a", "h-b", now, "x", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, st, "tenant_a", "h-b"); !closeTo(got.TerminatingUntil, now.Add(TerminatingHoldMax)) {
		t.Fatalf("hold = %v, want clamped to %v", got.TerminatingUntil, now.Add(TerminatingHoldMax))
	}
	reserveAt(t, st, "h-b2", "tenant_a", now, time.Hour, Placement{"acct-2", "browser-7"})
	if _, err := claimAt(t, st, "h-b2", now.Add(TerminatingHoldMax-time.Second), Placement{"acct-2", "browser-7"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim 1 s before the hold elapses = %v, want ErrConflict", err)
	}
	if ids, err := st.SweepExpired(ctx, now.Add(TerminatingHoldMax+time.Second)); err != nil || len(ids) != 0 {
		t.Fatalf("sweep = %v err=%v: an elapsed hold is cleaned, never reported as swept", ids, err)
	}
	if got := mustGet(t, st, "tenant_a", "h-b"); !got.TerminatingUntil.IsZero() {
		t.Fatalf("sweeper left the elapsed hold: %v", got.TerminatingUntil)
	}
	if c, err := claimAt(t, st, "h-b2", now.Add(TerminatingHoldMax+2*time.Second), Placement{"acct-2", "browser-7"}); err != nil || c.Status != StatusConnecting {
		t.Fatalf("claim after the hold elapsed = %+v err=%v", c, err)
	}

	// hold <= 0 is plain Terminate; a queued session has nothing to hold and a
	// repeated BeginTerminate keeps the first hold.
	reserveAt(t, st, "h-d", "tenant_a", leaseNow, time.Hour, Placement{"acct-4", "browser-4"})
	if err := st.BeginTerminate(ctx, "tenant_a", "h-d", now, "x", 0); err != nil {
		t.Fatal(err)
	}
	if next := reserveAt(t, st, "h-d2", "tenant_a", now, time.Hour, Placement{"acct-4", "browser-4"}); next.Status != StatusConnecting {
		t.Fatalf("hold=0 must release at once: %+v", next)
	}
	if err := st.BeginTerminate(ctx, "tenant_a", "h-blocked-1", now, "queued_cancel", TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, st, "tenant_a", "h-blocked-1"); got.Status != StatusTerminated || !got.TerminatingUntil.IsZero() {
		t.Fatalf("a queued session holds nothing: %+v", got)
	}
	if err := st.BeginTerminate(ctx, "tenant_a", "h-d2", now, "first", TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginTerminate(ctx, "tenant_a", "h-d2", now.Add(10*time.Second), "second", TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, st, "tenant_a", "h-d2"); got.LastError != "first" || !closeTo(got.TerminatingUntil, now.Add(TerminatingHoldMax)) {
		t.Fatalf("repeated BeginTerminate changed the hold: %+v", got)
	}
}

// A node-hosted session's hold is acked by that node only; a replaced node's
// ack is refused so it cannot free the lease its successor now owns.
func testTerminatingHoldIsFenced(t *testing.T, st Store) {
	ctx := t.Context()
	reserveAt(t, st, "n-a", "tenant_a", leaseNow, time.Hour, Placement{"acct-6", "browser-6"})
	if _, err := st.BindNode(ctx, "tenant_a", "n-a", "node-old", time.Minute, leaseNow); err != nil {
		t.Fatal(err)
	}
	bound, err := st.BindNode(ctx, "tenant_a", "n-a", "node-new", time.Minute, leaseNow)
	if err != nil || bound.LeaseGeneration != 2 {
		t.Fatalf("rebind = %+v err=%v", bound, err)
	}
	now := leaseNow.Add(time.Second)
	if err := st.BeginTerminate(ctx, "tenant_a", "n-a", now, "x", TerminatingHoldMax); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		fence LeaseFence
		want  error
	}{
		{"no fence", LeaseFence{}, ErrNodeMismatch},
		{"replaced node", LeaseFence{"node-old", 1}, ErrNodeMismatch},
		{"stale generation", LeaseFence{"node-new", 1}, ErrStaleGeneration},
	} {
		if err := st.ReleaseHold(ctx, "tenant_a", "n-a", c.fence, now); !errors.Is(err, c.want) {
			t.Errorf("%s: ack = %v, want %v", c.name, err, c.want)
		}
	}
	if s := reserveAt(t, st, "n-b", "tenant_a", now, time.Hour, Placement{"acct-6", "browser-6"}); s.Status != StatusQueued {
		t.Fatalf("a refused ack freed the lease: %+v", s)
	}
	if err := st.ReleaseHold(ctx, "tenant_a", "n-a", LeaseFence{"node-new", 2}, now); err != nil {
		t.Fatalf("holder ack: %v", err)
	}
	if c, err := claimAt(t, st, "n-b", now.Add(time.Second), Placement{"acct-6", "browser-6"}); err != nil || c.Status != StatusConnecting {
		t.Fatalf("claim after the holder's ack = %+v err=%v", c, err)
	}
}

// The session object clients receive never carries the node, the generation,
// the media lease or the hold. The golden pins the exact wire shape.
func TestSessionJSONCarriesNoInternalLeaseState(t *testing.T) {
	s := Session{
		ID: "voice_1", TenantID: "tenant_a", AppID: "app_a", Target: "chatgpt_web", Mode: ModeLive,
		Status: StatusConnected, IdentityRef: "acct-1", InstanceRef: "browser-1",
		CreatedAt: leaseNow, UpdatedAt: leaseNow, LeaseExpires: leaseNow.Add(time.Hour),
		NodeID: "node-secret-7", LeaseGeneration: 41, MediaLeaseExpires: leaseNow.Add(time.Minute),
		TerminatingUntil: leaseNow.Add(45 * time.Second),
	}
	got, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{"session_id":"voice_1","tenant_id":"tenant_a","app_id":"app_a","target":"chatgpt_web","mode":"live","status":"connected","muted":false,"identity_ref":"acct-1","instance_ref":"browser-1","created_at":"2026-10-06T12:00:00Z","updated_at":"2026-10-06T12:00:00Z","lease_expires_at":"2026-10-06T13:00:00Z","terminated_at":"0001-01-01T00:00:00Z"}`
	if string(got) != golden {
		t.Fatalf("session JSON changed:\n got %s\nwant %s", got, golden)
	}
}

// A voice.db created before the lease columns gains them in place on Ready and
// keeps its rows.
func TestSQLiteReadyAddsLeaseColumnsToAnOldDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE gateway_voice_sessions (
			session_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, app_id TEXT NOT NULL DEFAULT '',
			target TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'live', job_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'queued', muted INTEGER NOT NULL DEFAULT 0,
			identity_ref TEXT NOT NULL DEFAULT '', instance_ref TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			lease_expires_at TEXT NOT NULL DEFAULT '', terminated_at TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO gateway_voice_sessions (session_id, tenant_id, target, status, identity_ref, instance_ref, created_at, updated_at, lease_expires_at)
		 VALUES ('old', 'tenant_a', 'chatgpt_web', 'connected', 'acct-1', 'browser-1', '2026-10-06T12:00:00.000000000Z', '2026-10-06T12:00:00.000000000Z', '2026-10-06T13:00:00.000000000Z')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	st := NewSQLiteStore(db)
	for i := 0; i < 2; i++ { // the second Ready proves the upgrade is idempotent
		if err := st.Ready(t.Context()); err != nil {
			t.Fatalf("ready #%d: %v", i+1, err)
		}
	}
	got := mustGet(t, st, "tenant_a", "old")
	if got.Status != StatusConnected || got.NodeID != "" || got.LeaseGeneration != 0 || !got.TerminatingUntil.IsZero() {
		t.Fatalf("upgraded row = %+v", got)
	}
	if _, err := st.BindNode(t.Context(), "tenant_a", "old", "node-a", time.Minute, leaseNow); err != nil {
		t.Fatalf("bind on an upgraded database: %v", err)
	}
}
