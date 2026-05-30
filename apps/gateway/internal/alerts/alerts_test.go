package alerts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeSink struct {
	mu       chan struct{}
	received []Alert
	err      error
}

func newFakeSink(err error) *fakeSink {
	return &fakeSink{mu: make(chan struct{}, 1), err: err}
}

func (f *fakeSink) Send(_ context.Context, alert Alert) error {
	f.received = append(f.received, alert)
	return f.err
}

func newSyncManager(store Store, sink AlertSink) *Manager {
	m := NewManager(store, sink, nil, ConfigSummary{})
	m.dispatchSync = true
	return m
}

func TestMemoryStoreRaiseDedupesActiveAlerts(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	base := Alert{TenantID: "t1", JobID: "job-1", Kind: KindCaptcha, Message: "solve it"}

	first, created, err := store.Raise(ctx, base)
	if err != nil || !created {
		t.Fatalf("first raise: created=%v err=%v", created, err)
	}
	if first.Status != StatusOpen {
		t.Fatalf("expected open status, got %q", first.Status)
	}

	second, created, err := store.Raise(ctx, base)
	if err != nil {
		t.Fatalf("second raise err: %v", err)
	}
	if created {
		t.Fatalf("expected dedupe (created=false) for active alert")
	}
	if second.AlertID != first.AlertID {
		t.Fatalf("dedupe returned different alert: %q vs %q", second.AlertID, first.AlertID)
	}

	// After resolution a new alert for the same triple may be raised.
	if _, _, err := store.UpdateStatus(ctx, "t1", first.AlertID, StatusResolved, time.Now()); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, created, err = store.Raise(ctx, base)
	if err != nil || !created {
		t.Fatalf("post-resolve raise: created=%v err=%v", created, err)
	}
}

func TestManagerRaiseManualActionDispatchesAndMarksNotified(t *testing.T) {
	store := NewMemoryStore()
	sink := newFakeSink(nil)
	manager := newSyncManager(store, sink)
	ctx := context.Background()

	alert, err := manager.RaiseManualAction(ctx, Alert{TenantID: "t1", JobID: "job-1", Kind: KindManualLogin})
	if err != nil {
		t.Fatalf("raise: %v", err)
	}
	if len(sink.received) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(sink.received))
	}

	got, found, err := store.Get(ctx, "t1", alert.AlertID)
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.Status != StatusNotified {
		t.Fatalf("expected notified status after dispatch, got %q", got.Status)
	}

	// A duplicate raise must not dispatch again.
	if _, err := manager.RaiseManualAction(ctx, Alert{TenantID: "t1", JobID: "job-1", Kind: KindManualLogin}); err != nil {
		t.Fatalf("duplicate raise: %v", err)
	}
	if len(sink.received) != 1 {
		t.Fatalf("expected dedupe to suppress second notification, got %d", len(sink.received))
	}
}

func TestManagerNilSafe(t *testing.T) {
	var manager *Manager
	if _, err := manager.RaiseManualAction(context.Background(), Alert{}); err != nil {
		t.Fatalf("nil manager raise should be a no-op, got %v", err)
	}
}

func TestMultiSinkFansOutAndJoinsErrors(t *testing.T) {
	ok := newFakeSink(nil)
	bad := newFakeSink(errors.New("boom"))
	multi := NewMultiSink(ok, nil, bad)

	err := multi.Send(context.Background(), Alert{JobID: "job-1"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected joined error containing boom, got %v", err)
	}
	if len(ok.received) != 1 || len(bad.received) != 1 {
		t.Fatalf("expected both sinks attempted: ok=%d bad=%d", len(ok.received), len(bad.received))
	}
}

func TestManagerAcknowledgeAndResolve(t *testing.T) {
	store := NewMemoryStore()
	manager := newSyncManager(store, newFakeSink(nil))
	ctx := context.Background()
	alert, err := manager.RaiseManualAction(ctx, Alert{TenantID: "t1", JobID: "job-1", Kind: KindVerification})
	if err != nil {
		t.Fatalf("raise: %v", err)
	}

	acked, found, err := manager.Acknowledge(ctx, "t1", alert.AlertID)
	if err != nil || !found {
		t.Fatalf("acknowledge: found=%v err=%v", found, err)
	}
	if acked.Status != StatusAcknowledged || acked.AckedAt.IsZero() {
		t.Fatalf("acknowledge did not stamp state: %+v", acked)
	}

	resolved, found, err := manager.Resolve(ctx, "t1", alert.AlertID)
	if err != nil || !found {
		t.Fatalf("resolve: found=%v err=%v", found, err)
	}
	if resolved.Status != StatusResolved || resolved.ResolvedAt.IsZero() {
		t.Fatalf("resolve did not stamp state: %+v", resolved)
	}

	// Unknown alert returns found=false, not an error.
	if _, found, err := manager.Resolve(ctx, "t1", "missing"); err != nil || found {
		t.Fatalf("expected not-found for missing alert, found=%v err=%v", found, err)
	}
}

func TestManagerListFiltersByStatus(t *testing.T) {
	store := NewMemoryStore()
	manager := newSyncManager(store, newFakeSink(nil))
	ctx := context.Background()
	a1, _ := manager.RaiseManualAction(ctx, Alert{TenantID: "t1", JobID: "job-1", Kind: KindCaptcha})
	_, _ = manager.RaiseManualAction(ctx, Alert{TenantID: "t1", JobID: "job-2", Kind: KindCaptcha})
	if _, _, err := manager.Resolve(ctx, "t1", a1.AlertID); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	resolved, err := manager.List(ctx, Filter{TenantID: "t1", Status: StatusResolved})
	if err != nil {
		t.Fatalf("list resolved: %v", err)
	}
	if len(resolved) != 1 || resolved[0].AlertID != a1.AlertID {
		t.Fatalf("expected only the resolved alert, got %+v", resolved)
	}

	all, err := manager.List(ctx, Filter{TenantID: "t1"})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(all))
	}
}

func TestNormalizeKindAllowlist(t *testing.T) {
	cases := map[string]string{
		"CAPTCHA":         KindCaptcha,
		" manual_login ":  KindManualLogin,
		"verification":    KindVerification,
		"drift":           KindDrift,
		"something-weird": KindOther,
		"":                KindOther,
	}
	for in, want := range cases {
		if got := normalizeKind(in); got != want {
			t.Fatalf("normalizeKind(%q)=%q want %q", in, got, want)
		}
	}
}
