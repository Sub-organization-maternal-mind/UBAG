package serve

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/voice"
	"github.com/ubag/ubag/apps/gateway/internal/voiceplace"
)

// voiceNodeReleaseInterval is how often node slots of ended sessions are
// returned. Every path that ends a session on this replica releases at once;
// this covers the rest (a lease sweep, a peer failure, a terminate served by
// another replica).
const voiceNodeReleaseInterval = 5 * time.Second

// newVoiceNodePlacer builds the helper-hosted voice placer (P5.9, ADR-0017)
// behind UBAG_HELPER_VOICE: nil unless the whole flag ladder is on and voice
// sessions are configured. With the ladder on but no fleet to place on there is
// nothing safe to do (an account that lives on a node must never be hosted on the
// primary), so that is a startup error, not a silent fallback.
func newVoiceNodePlacer(fleet *helperFleet, voiceStore voice.Store) (*voiceplace.Placer, error) {
	if voiceStore == nil || !voice.HelperVoiceEnabled(os.Getenv) {
		return nil, nil
	}
	if fleet == nil {
		return nil, fmt.Errorf("UBAG_HELPER_VOICE=true requires helper placement (UBAG_HELPER_DISPATCH with the node store)")
	}
	return &voiceplace.Placer{Fleet: fleet.placer, Profiles: fleet.picker.Profiles}, nil
}

// runVoiceNodeRelease returns the node slot and identity lane of every ended
// voice session, until ctx ends. A nil placer does nothing.
func runVoiceNodeRelease(ctx context.Context, p *voiceplace.Placer, store voice.Store) {
	if p == nil {
		return
	}
	ticker := time.NewTicker(voiceNodeReleaseInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			p.ReleaseEnded(cctx, store, time.Now().UTC())
			cancel()
		}
	}
}
