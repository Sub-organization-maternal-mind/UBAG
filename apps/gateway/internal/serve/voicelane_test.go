package serve

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
	"github.com/ubag/ubag/apps/gateway/internal/voice"

	_ "modernc.org/sqlite"
)

// UBAG_VOICE_LANE_EXCLUSION is opt-in: unset, empty or falsy leaves it off even
// when voice sessions are configured; only an explicit truthy value turns it on.
func TestVoiceLaneExclusionIsOptIn(t *testing.T) {
	store := voice.NewMemoryStore()
	registry := topology.NewConcurrencyRegistry()

	for _, off := range []string{"", "0", "false", "no", "off", " OFF ", "anything-else"} {
		t.Setenv("UBAG_VOICE_LANE_EXCLUSION", off)
		if wireVoiceLaneExclusion(store, topology.NewMemoryStore(), registry, nil) != nil {
			t.Fatalf("%q must leave exclusion off", off)
		}
	}
	for _, on := range []string{"1", "true", "yes", " TRUE "} {
		t.Setenv("UBAG_VOICE_LANE_EXCLUSION", on)
		if wireVoiceLaneExclusion(store, topology.NewMemoryStore(), registry, nil) == nil {
			t.Fatalf("%q must switch exclusion on", on)
		}
	}
	// No voice sessions configured (UBAG_VOICE_STORE=disabled): nothing to exclude.
	t.Setenv("UBAG_VOICE_LANE_EXCLUSION", "true")
	if wireVoiceLaneExclusion(nil, topology.NewMemoryStore(), registry, nil) != nil {
		t.Fatal("no voice store: no exclusion")
	}
}

// A shared (postgres) voice store moves the browser-lane registrations into the
// shared admission store; a per-process one keeps them local.
func TestVoiceLaneRegistrationsAreSharedOnlyWithASharedVoiceStore(t *testing.T) {
	t.Setenv("UBAG_VOICE_LANE_EXCLUSION", "true")
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "admission.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	backend := topology.NewSQLiteTokenBackend(db)
	if err := backend.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	liveJobs := func() int {
		t.Helper()
		counts, err := backend.LaneKindCounts(t.Context(), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return counts["lane-job"]
	}

	for _, c := range []struct {
		voiceStore string
		wantShared bool
	}{{"memory", false}, {"sqlite", false}, {"postgres", true}, {"postgresql", true}} {
		t.Setenv("UBAG_VOICE_STORE", c.voiceStore)
		registry := topology.NewConcurrencyRegistry()
		if wireVoiceLaneExclusion(voice.NewMemoryStore(), topology.NewMemoryStore(), registry, backend) == nil {
			t.Fatalf("%s: exclusion should be on", c.voiceStore)
		}
		before := liveJobs()
		hold, err := registry.EnterLane(t.Context(), topology.LaneJob, "browser:b:1")
		if err != nil || hold == nil {
			t.Fatalf("%s: EnterLane hold=%v err=%v", c.voiceStore, hold, err)
		}
		if shared := liveJobs() == before+1; shared != c.wantShared {
			t.Fatalf("UBAG_VOICE_STORE=%s: registrations in the shared store = %v, want %v", c.voiceStore, shared, c.wantShared)
		}
		hold.Release()
	}

	// A shared voice store with the shared admission store off stays local (and warns).
	t.Setenv("UBAG_VOICE_STORE", "postgres")
	registry := topology.NewConcurrencyRegistry()
	if wireVoiceLaneExclusion(voice.NewMemoryStore(), topology.NewMemoryStore(), registry, nil) == nil {
		t.Fatal("exclusion should still be on")
	}
	hold, err := registry.EnterLane(t.Context(), topology.LaneJob, "browser:b:1")
	if err != nil || hold == nil {
		t.Fatalf("local fallback: hold=%v err=%v", hold, err)
	}
	hold.Release()
}
