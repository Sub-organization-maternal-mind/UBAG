package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	"github.com/ubag/ubag/apps/gateway/internal/voice"
)

func TestVoiceNodePlacerFromEnv(t *testing.T) {
	ctx := context.Background()
	for _, k := range []string{"UBAG_HELPER_NODES", "UBAG_HELPER_PLANE", "UBAG_HELPER_DISPATCH", "UBAG_HELPER_VOICE"} {
		t.Setenv(k, "")
	}
	// The fleet the dispatch wiring builds over the memory node store.
	t.Setenv("UBAG_HELPER_DISPATCH", "true")
	fleet, err := newHelperFleetFromEnv(ctx, nodes.NewMemoryStore(), nil, "memory", nil)
	if err != nil || fleet == nil {
		t.Fatalf("fleet = %v err = %v", fleet, err)
	}
	store := voice.NewMemoryStore()
	ladder := func(t *testing.T) {
		for _, k := range []string{"UBAG_HELPER_NODES", "UBAG_HELPER_PLANE", "UBAG_HELPER_DISPATCH", "UBAG_HELPER_VOICE"} {
			t.Setenv(k, "true")
		}
	}

	t.Run("off by default: every call is hosted on the primary", func(t *testing.T) {
		if p, err := newVoiceNodePlacer(fleet, store); p != nil || err != nil {
			t.Fatalf("placer = %v err = %v", p, err)
		}
	})
	t.Run("part of the ladder is not enough", func(t *testing.T) {
		t.Setenv("UBAG_HELPER_VOICE", "true")
		if p, err := newVoiceNodePlacer(fleet, store); p != nil || err != nil {
			t.Fatalf("placer = %v err = %v", p, err)
		}
	})
	t.Run("the whole ladder places on the dispatch fleet", func(t *testing.T) {
		ladder(t)
		p, err := newVoiceNodePlacer(fleet, store)
		if err != nil || p == nil || p.Fleet != fleet.placer || p.Profiles == nil {
			t.Fatalf("placer = %+v err = %v", p, err)
		}
	})
	t.Run("no voice sessions, nothing to place", func(t *testing.T) {
		ladder(t)
		if p, err := newVoiceNodePlacer(fleet, nil); p != nil || err != nil {
			t.Fatalf("placer = %v err = %v", p, err)
		}
	})
	t.Run("the ladder without a fleet is a startup error, never a primary fallback", func(t *testing.T) {
		ladder(t)
		p, err := newVoiceNodePlacer(nil, store)
		if p != nil || err == nil || !strings.Contains(err.Error(), "UBAG_HELPER_VOICE") {
			t.Fatalf("placer = %v err = %v", p, err)
		}
	})
}

func TestVoiceNodeReleaseStopsWithItsContextAndToleratesNoPlacer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runVoiceNodeRelease(ctx, nil, voice.NewMemoryStore()); close(done) }()
	select {
	case <-done: // a nil placer returns at once
	case <-time.After(time.Second):
		t.Fatal("a nil placer must not loop")
	}
	cancel()
}
