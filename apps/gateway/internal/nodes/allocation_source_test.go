package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeManager is the in-test stand-in for the external Fleet Manager.
type fakeManager struct {
	mu        sync.Mutex
	body      string
	etag      string
	status    int  // 0 = normal
	down      bool // close the connection without answering
	ifNone    []string
	redirect  string
	bigBody   bool
	hitCount  int
	srv       *httptest.Server
	lastCalls int
}

func newFakeManager(t *testing.T) *fakeManager {
	m := &fakeManager{}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *fakeManager) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hitCount++
	m.ifNone = append(m.ifNone, r.Header.Get("If-None-Match"))
	switch {
	case m.down:
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Close()
	case m.redirect != "":
		http.Redirect(w, r, m.redirect, http.StatusFound)
	case m.status != 0:
		w.WriteHeader(m.status)
	case m.bigBody:
		w.Write([]byte(strings.Repeat(" ", maxResponseBytes+10)))
	case m.etag != "" && r.Header.Get("If-None-Match") == m.etag:
		w.WriteHeader(http.StatusNotModified)
	default:
		w.Header().Set("ETag", m.etag)
		w.Write([]byte(m.body))
	}
}

func (m *fakeManager) set(body, etag string) {
	m.mu.Lock()
	m.body, m.etag = body, etag
	m.mu.Unlock()
}

func (m *fakeManager) with(f func(*fakeManager)) {
	m.mu.Lock()
	f(m)
	m.mu.Unlock()
}

func wireEntry(id string, gen int64, workloads int, state string) map[string]any {
	return map[string]any{
		"schema_version": 1, "node_id": id, "region": "eu-west", "endpoint": "10.8.0.2:7443",
		"cert_identity": map[string]any{"uri_san": NodeURISAN(id)},
		"cpu_millis":    3000, "memory_bytes": 5 << 30, "reservation_state": "known", "state": state,
		"max_browser_workloads": workloads, "voice_capable": false,
		"valid_until": t0.Add(24 * time.Hour).Format(time.RFC3339), "generation": gen,
	}
}

func wireList_(entries ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"schema_version": 1, "generated_at": t0.Format(time.RFC3339), "allocations": entries,
	})
	return string(b)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestPoller(t *testing.T, m *fakeManager, grace time.Duration) (*Poller, *MemoryStore, *clock) {
	t.Helper()
	src, err := NewHTTPSource(m.srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, clk := NewMemoryStore(), &clock{t: t0}
	return NewPoller(src, st, PollerConfig{Interval: time.Second, StaleGrace: grace}, clk.now), st, clk
}

func current(t *testing.T, p *Poller, now time.Time) map[string]Allocation {
	t.Helper()
	all, err := p.Current(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Allocation{}
	for _, a := range all {
		out[a.NodeID] = a
	}
	return out
}

func TestAllocationSourceETagAndApply(t *testing.T) {
	m := newFakeManager(t)
	m.set(wireList_(wireEntry("helper-1", 1, 2, "active")), `"v1"`)
	p, st, _ := newTestPoller(t, m, time.Minute)
	ctx := context.Background()

	if err := p.Poll(ctx); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	a, err := st.GetAllocation(ctx, "helper-1")
	if err != nil || a.MaxBrowserWorkloads != 2 || a.Generation != 1 {
		t.Fatalf("stored = %+v, %v", a, err)
	}
	if err := p.Poll(ctx); err != nil { // 304 path
		t.Fatalf("second poll: %v", err)
	}
	if got := m.ifNone; len(got) != 2 || got[0] != "" || got[1] != `"v1"` {
		t.Fatalf("If-None-Match sequence = %q", got)
	}

	m.set(wireList_(wireEntry("helper-1", 2, 3, "active")), `"v2"`)
	if err := p.Poll(ctx); err != nil {
		t.Fatalf("third poll: %v", err)
	}
	if a, _ := st.GetAllocation(ctx, "helper-1"); a.Generation != 2 || a.MaxBrowserWorkloads != 3 {
		t.Fatalf("not updated: %+v", a)
	}
}

func TestAllocationSourceParseFailureFailsClosed(t *testing.T) {
	good := wireEntry("helper-1", 1, 2, "active")
	spoof := wireEntry("helper-2", 1, 2, "active")
	spoof["cert_identity"] = map[string]any{"uri_san": NodeURISAN("someone-else")}
	extra := wireEntry("helper-2", 1, 2, "active")
	extra["surprise"] = true
	badVersion := wireEntry("helper-2", 1, 2, "active")
	badVersion["schema_version"] = 2
	missing := wireEntry("helper-2", 1, 2, "active")
	delete(missing, "valid_until")
	cases := map[string]string{
		"not json":          `<html>`,
		"trailing data":     wireList_(good) + `{}`,
		"unknown field":     wireList_(good, extra),
		"entry version":     wireList_(good, badVersion),
		"missing required":  wireList_(good, missing),
		"uri san mismatch":  wireList_(good, spoof),
		"duplicate node":    wireList_(good, good),
		"list version":      strings.Replace(wireList_(good), `"schema_version":1}`, `"schema_version":2}`, 1),
		"no generated_at":   `{"schema_version":1,"allocations":[]}`,
		"allocations null":  `{"schema_version":1,"generated_at":"2026-10-06T12:00:00Z","allocations":null}`,
		"too many entries":  tooMany(),
		"bad udp for voice": strings.Replace(wireList_(good), `"voice_capable":false`, `"voice_capable":true`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			m := newFakeManager(t)
			m.set(body, `"bad"`)
			p, st, _ := newTestPoller(t, m, time.Minute)
			err := p.Poll(context.Background())
			if !errors.Is(err, ErrBadResponse) {
				t.Fatalf("err = %v, want ErrBadResponse", err)
			}
			if all, _ := st.ListAllocations(context.Background()); len(all) != 0 {
				t.Fatalf("partial apply of a rejected list: %+v", all)
			}
			if p.etag != "" || !p.lastSuccess.IsZero() {
				t.Fatal("bad response advanced etag or freshness")
			}
		})
	}
}

func tooMany() string {
	var es []map[string]any
	for i := 0; i <= MaxNodes; i++ {
		es = append(es, wireEntry("n"+string(rune('a'+i%26))+strings.Repeat("x", i/26), 1, 1, "active"))
	}
	return wireList_(es...)
}

func TestAllocationSourceBadResponseKeepsLastKnownGood(t *testing.T) {
	m := newFakeManager(t)
	m.set(wireList_(wireEntry("helper-1", 1, 2, "active")), `"v1"`)
	p, _, clk := newTestPoller(t, m, time.Minute)
	ctx := context.Background()
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	m.set(`{"garbage":`, `"v2"`)
	clk.t = t0.Add(30 * time.Second)
	if err := p.Poll(ctx); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("err = %v", err)
	}
	if p.etag != `"v1"` || !p.lastSuccess.Equal(t0) {
		t.Fatalf("etag=%q lastSuccess=%v: a bad response must not refresh anything", p.etag, p.lastSuccess)
	}
	if got := current(t, p, clk.t)["helper-1"]; got.State != StateActive || got.MaxBrowserWorkloads != 2 {
		t.Fatalf("last-known-good lost: %+v", got)
	}
}

func TestAllocationSourceManagerDown(t *testing.T) {
	m := newFakeManager(t)
	m.set(wireList_(wireEntry("helper-1", 1, 2, "active")), `"v1"`)
	p, _, clk := newTestPoller(t, m, 5*time.Minute)
	ctx := context.Background()
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}

	// Manager goes down and, behind the outage, would have raised the grant.
	m.with(func(m *fakeManager) { m.down = true })
	m.set(wireList_(wireEntry("helper-1", 2, 8, "active")), `"v2"`)

	clk.t = t0.Add(4 * time.Minute) // inside grace
	if err := p.Poll(ctx); err == nil || errors.Is(err, ErrBadResponse) {
		t.Fatalf("down poll err = %v, want transport error", err)
	}
	g := current(t, p, clk.t)["helper-1"]
	if g.State != StateActive || g.MaxBrowserWorkloads != 2 || g.Generation != 1 {
		t.Fatalf("inside grace must keep exactly the last grant, got %+v", g)
	}
	d := Evaluate(g.Grant(), clk.t.Add(-time.Second), clk.t, Pressure{}, 2)
	if !d.Eligible || d.Limit != 2 {
		t.Fatalf("placement inside grace = %+v", d)
	}

	clk.t = t0.Add(5*time.Minute + time.Second) // past grace
	_ = p.Poll(ctx)
	g = current(t, p, clk.t)["helper-1"]
	if g.State != StateDraining || g.MaxBrowserWorkloads != 2 {
		t.Fatalf("past grace must stop new placements without raising: %+v", g)
	}
	if d := Evaluate(g.Grant(), clk.t.Add(-time.Second), clk.t, Pressure{}, 2); d.Eligible || d.Reason != ReasonDraining {
		t.Fatalf("placement past grace = %+v", d)
	}

	// Recovery: the manager returns and its newer grant is accepted.
	m.with(func(m *fakeManager) { m.down = false })
	clk.t = t0.Add(10 * time.Minute)
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if g = current(t, p, clk.t)["helper-1"]; g.State != StateActive || g.MaxBrowserWorkloads != 8 {
		t.Fatalf("after recovery: %+v", g)
	}
}

func TestAllocationSourceTransportStatusFailures(t *testing.T) {
	for name, set := range map[string]func(*fakeManager){
		"500":         func(m *fakeManager) { m.status = 500 },
		"404":         func(m *fakeManager) { m.status = 404 },
		"redirect":    func(m *fakeManager) { m.redirect = "http://127.0.0.1:1/" },
		"oversized":   func(m *fakeManager) { m.bigBody = true },
		"304 no etag": func(m *fakeManager) { m.status = http.StatusNotModified },
	} {
		t.Run(name, func(t *testing.T) {
			m := newFakeManager(t)
			m.with(set)
			p, st, _ := newTestPoller(t, m, time.Minute)
			if err := p.Poll(context.Background()); !errors.Is(err, ErrBadResponse) {
				t.Fatalf("err = %v, want ErrBadResponse", err)
			}
			if all, _ := st.ListAllocations(context.Background()); len(all) != 0 || !p.lastSuccess.IsZero() {
				t.Fatal("failed poll changed state")
			}
		})
	}
}

func TestAllocationSourceStaleGenerationSkippedNotFatal(t *testing.T) {
	m := newFakeManager(t)
	m.set(wireList_(wireEntry("helper-1", 5, 4, "active")), `"v1"`)
	p, st, _ := newTestPoller(t, m, time.Minute)
	ctx := context.Background()
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	// Manager regresses helper-1 (stale gen) and adds helper-2.
	m.set(wireList_(wireEntry("helper-1", 4, 9, "active"), wireEntry("helper-2", 1, 1, "active")), `"v2"`)
	if err := p.Poll(ctx); err != nil {
		t.Fatalf("a fenced entry must not fail the poll: %v", err)
	}
	if a, _ := st.GetAllocation(ctx, "helper-1"); a.Generation != 5 || a.MaxBrowserWorkloads != 4 {
		t.Fatalf("stale generation accepted: %+v", a)
	}
	if _, err := st.GetAllocation(ctx, "helper-2"); err != nil {
		t.Fatalf("good sibling not applied: %v", err)
	}
}

func TestAllocationSourceAbsentNodeStopsNewPlacements(t *testing.T) {
	m := newFakeManager(t)
	m.set(wireList_(wireEntry("helper-1", 1, 2, "active"), wireEntry("helper-2", 1, 2, "active")), `"v1"`)
	p, _, clk := newTestPoller(t, m, time.Hour)
	ctx := context.Background()
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	m.set(wireList_(wireEntry("helper-1", 1, 2, "active")), `"v2"`)
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	got := current(t, p, clk.t)
	if got["helper-1"].State != StateActive || got["helper-2"].State != StateDraining {
		t.Fatalf("states = %s / %s", got["helper-1"].State, got["helper-2"].State)
	}
	// A 304 keeps the absent set; the node returns when the manager lists it.
	m.set(wireList_(wireEntry("helper-1", 1, 2, "active"), wireEntry("helper-2", 1, 2, "active")), `"v3"`)
	if err := p.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if got = current(t, p, clk.t); got["helper-2"].State != StateActive {
		t.Fatalf("relisted node still draining: %+v", got["helper-2"])
	}
}

func TestAllocationSourceRestartUsesAcceptedAtForGrace(t *testing.T) {
	m := newFakeManager(t)
	m.with(func(m *fakeManager) { m.down = true })
	p, st, clk := newTestPoller(t, m, 5*time.Minute)
	// A persisted grant from a previous process.
	if err := st.ApplyAllocation(context.Background(), goodAlloc("helper-1"), t0); err != nil {
		t.Fatal(err)
	}
	_ = p.Poll(context.Background()) // fails, no freshness
	clk.t = t0.Add(4 * time.Minute)
	if got := current(t, p, clk.t)["helper-1"]; got.State != StateActive {
		t.Fatalf("persisted grant dropped inside grace: %+v", got)
	}
	clk.t = t0.Add(6 * time.Minute)
	if got := current(t, p, clk.t)["helper-1"]; got.State != StateDraining {
		t.Fatalf("persisted grant kept past grace: %+v", got)
	}
}

func TestAllocationSourceRevokedPassesThrough(t *testing.T) {
	m := newFakeManager(t)
	m.set(wireList_(wireEntry("helper-1", 1, 2, "revoked")), `"v1"`)
	p, _, clk := newTestPoller(t, m, time.Minute)
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	clk.t = t0.Add(time.Hour)
	if got := current(t, p, clk.t)["helper-1"]; got.State != StateRevoked {
		t.Fatalf("revoked must stay revoked, got %s", got.State)
	}
}

func TestAllocationSourceRunStopsOnCancel(t *testing.T) {
	m := newFakeManager(t)
	m.set(wireList_(wireEntry("helper-1", 1, 2, "active")), `"v1"`)
	src, _ := NewHTTPSource(m.srv.URL, nil)
	st := NewMemoryStore()
	p := NewPoller(src, st, PollerConfig{Interval: 10 * time.Millisecond, StaleGrace: time.Minute}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		m.mu.Lock()
		n := m.hitCount
		m.mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("poller did not poll repeatedly")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestAllocationSourceNewHTTPSourceRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "ftp://x", "http://", "http://user:pw@host/", "://bad", "host:80"} {
		if _, err := NewHTTPSource(u, nil); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
}

func TestAllocationSourcePollerConfigFromEnv(t *testing.T) {
	env := func(kv map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
	}
	if _, _, ok, err := PollerConfigFromEnv(env(nil)); ok || err != nil {
		t.Fatalf("unset url: ok=%v err=%v", ok, err)
	}
	u, cfg, ok, err := PollerConfigFromEnv(env(map[string]string{EnvFleetManagerURL: "http://10.8.0.1:9000/v1/fleet/allocations"}))
	if !ok || err != nil || u == "" || cfg.Interval != DefaultPollInterval || cfg.StaleGrace != DefaultStaleGrace {
		t.Fatalf("defaults: %q %+v %v %v", u, cfg, ok, err)
	}
	_, cfg, ok, err = PollerConfigFromEnv(env(map[string]string{
		EnvFleetManagerURL: "http://m", EnvFleetPollSeconds: "10", EnvFleetStaleGraceSec: "0",
	}))
	if !ok || err != nil || cfg.Interval != 10*time.Second || cfg.StaleGrace != 0 {
		t.Fatalf("overrides: %+v %v %v", cfg, ok, err)
	}
	for _, kv := range []map[string]string{
		{EnvFleetPollSeconds: "4"}, {EnvFleetPollSeconds: "3601"}, {EnvFleetPollSeconds: "abc"},
		{EnvFleetStaleGraceSec: "-1"}, {EnvFleetStaleGraceSec: "86401"},
	} {
		kv[EnvFleetManagerURL] = "http://m"
		if _, _, ok, err := PollerConfigFromEnv(env(kv)); ok || err == nil {
			t.Errorf("accepted %v", kv)
		}
	}
}
