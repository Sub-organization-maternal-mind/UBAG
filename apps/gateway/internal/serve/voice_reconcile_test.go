package serve

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/voice"
)

type fakeVoiceStore struct {
	voice.Store // unimplemented methods panic; the reconciler only calls Get
	session     voice.Session
	found       bool
	err         error
}

func (f *fakeVoiceStore) Get(context.Context, string, string) (voice.Session, bool, error) {
	return f.session, f.found, f.err
}

type fakeVoiceHub struct {
	sessions     []voice.Session
	disconnected []string
	muteCalls    []bool
}

func (h *fakeVoiceHub) Sessions() []voice.Session { return h.sessions }
func (h *fakeVoiceHub) Disconnect(id string)      { h.disconnected = append(h.disconnected, id) }
func (h *fakeVoiceHub) SetMuted(_ string, muted bool) bool {
	h.muteCalls = append(h.muteCalls, muted)
	return true
}

func newReconcilerFixture(failClosed bool) (*voiceReconciler, *fakeVoiceHub, *fakeVoiceStore) {
	live := voice.Session{ID: "vs_1", TenantID: "t1", Status: voice.StatusConnected}
	hub := &fakeVoiceHub{sessions: []voice.Session{live}}
	store := &fakeVoiceStore{session: live, found: true}
	return &voiceReconciler{hub: hub, store: store, failClosed: failClosed,
		errs: map[string]int{}, muted: map[string]bool{}}, hub, store
}

func TestVoiceReconcilerSyncsMutedToHub(t *testing.T) {
	r, hub, store := newReconcilerFixture(false)
	now := time.Now().UTC()

	r.step(context.Background(), now)
	if len(hub.muteCalls) != 0 {
		t.Fatalf("unchanged mute must not call SetMuted, got %v", hub.muteCalls)
	}
	store.session.Muted = true
	r.step(context.Background(), now)
	r.step(context.Background(), now) // idempotent: no repeat call
	if len(hub.muteCalls) != 1 || !hub.muteCalls[0] {
		t.Fatalf("SetMuted calls = %v, want [true]", hub.muteCalls)
	}
	store.session.Muted = false
	r.step(context.Background(), now)
	if len(hub.muteCalls) != 2 || hub.muteCalls[1] {
		t.Fatalf("SetMuted calls = %v, want [true false]", hub.muteCalls)
	}
}

func TestVoiceReconcilerDisconnectsTerminatedSession(t *testing.T) {
	r, hub, store := newReconcilerFixture(false)
	store.session.Status = voice.StatusTerminated
	r.step(context.Background(), time.Now().UTC())
	if len(hub.disconnected) != 1 {
		t.Fatalf("disconnected = %v, want one", hub.disconnected)
	}
}

func TestVoiceReconcilerStoreErrorsFailOpenByDefault(t *testing.T) {
	r, hub, store := newReconcilerFixture(false)
	store.err = errors.New("db down")
	for i := 0; i < voiceReconcileMaxStoreErrors*3; i++ {
		r.step(context.Background(), time.Now().UTC())
	}
	if len(hub.disconnected) != 0 {
		t.Fatalf("fail-open reconciler disconnected media: %v", hub.disconnected)
	}
}

func TestVoiceReconcilerFailClosedDisconnectsAfterGrace(t *testing.T) {
	r, hub, store := newReconcilerFixture(true)
	store.err = errors.New("db down")
	for i := 0; i < voiceReconcileMaxStoreErrors-1; i++ {
		r.step(context.Background(), time.Now().UTC())
	}
	if len(hub.disconnected) != 0 {
		t.Fatalf("disconnected inside the grace period: %v", hub.disconnected)
	}
	r.step(context.Background(), time.Now().UTC())
	if len(hub.disconnected) != 1 || hub.disconnected[0] != "vs_1" {
		t.Fatalf("disconnected = %v, want [vs_1] after %d errors", hub.disconnected, voiceReconcileMaxStoreErrors)
	}
}

func TestVoiceReconcilerSuccessResetsErrorCount(t *testing.T) {
	r, hub, store := newReconcilerFixture(true)
	store.err = errors.New("blip")
	for i := 0; i < voiceReconcileMaxStoreErrors-1; i++ {
		r.step(context.Background(), time.Now().UTC())
	}
	store.err = nil
	r.step(context.Background(), time.Now().UTC())
	store.err = errors.New("blip")
	for i := 0; i < voiceReconcileMaxStoreErrors-1; i++ {
		r.step(context.Background(), time.Now().UTC())
	}
	if len(hub.disconnected) != 0 {
		t.Fatalf("non-consecutive errors must not disconnect: %v", hub.disconnected)
	}
}

func TestVoiceStoreMemoryWithSQLGatewayWarns(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	t.Setenv("UBAG_VOICE_STORE", "memory")

	store, _, closeFn, err := newVoiceComponentsFromEnv(context.Background(), "postgres", nil, nil)
	if err != nil || store == nil {
		t.Fatalf("store=%v err=%v", store, err)
	}
	closeFn()
	if !strings.Contains(buf.String(), "UBAG_VOICE_STORE is memory") {
		t.Fatalf("expected memory-with-SQL-gateway warning, log: %q", buf.String())
	}

	buf.Reset()
	if _, _, closeFn, err := newVoiceComponentsFromEnv(context.Background(), "memory", nil, nil); err != nil {
		t.Fatal(err)
	} else {
		closeFn()
	}
	if strings.Contains(buf.String(), "UBAG_VOICE_STORE is memory") {
		t.Fatalf("unexpected warning for memory gateway: %q", buf.String())
	}
}

func TestVoiceStoreDisabledContainmentSwitch(t *testing.T) {
	t.Setenv("UBAG_VOICE_STORE", "disabled")
	store, media, _, err := newVoiceComponentsFromEnv(context.Background(), "postgres", nil, nil)
	if err != nil || store != nil || media != nil {
		t.Fatalf("disabled must yield no store/media: %v %v %v", store, media, err)
	}
}
