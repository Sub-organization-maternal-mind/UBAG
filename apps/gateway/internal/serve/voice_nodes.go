package serve

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
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

// newVoiceRemoteFromEnv builds the primary-side media plane of helper-hosted voice
// (P5.11, ADR-0018): the negotiator that offers a node-bound session's media to its
// Helper Node, fences every control call by the session's lease generation and
// supervises the call. nil unless the node placer exists (UBAG_HELPER_VOICE with the
// whole ladder on), so with the flag off every call is hosted on the primary as before.
//
// It adds no flag. It dials with the dispatch identity (UBAG_HELPER_CA_FILE,
// UBAG_HELPER_CLIENT_CERT_FILE, UBAG_HELPER_CLIENT_KEY_FILE, which the ladder already
// requires), checks the node's workload and adapter registry like job dispatch does,
// and derives per-attempt credentials from UBAG_VOICE_RELAY_SECRET (the secret that
// signs the primary's own relay hellos; it never leaves this process: a node only
// ever receives keys derived for one attempt). Without that secret no node call can
// be started, exactly as live media is unavailable on the primary.
func newVoiceRemoteFromEnv(store voice.Store, nodeStore nodes.Store, placer *voiceplace.Placer) (*voice.RemoteNegotiator, error) {
	if placer == nil || store == nil {
		return nil, nil
	}
	if nodeStore == nil {
		return nil, fmt.Errorf("UBAG_HELPER_VOICE=true requires the helper node store (UBAG_HELPER_NODES)")
	}
	var files [3]string
	for i, name := range []string{"UBAG_HELPER_CA_FILE", "UBAG_HELPER_CLIENT_CERT_FILE", "UBAG_HELPER_CLIENT_KEY_FILE"} {
		if files[i] = strings.TrimSpace(getenv(name, "")); files[i] == "" {
			return nil, fmt.Errorf("UBAG_HELPER_VOICE=true requires %s", name)
		}
	}
	dialer, err := newHelperDialer(files[0], files[1], files[2], nodeStore)
	if err != nil {
		return nil, err
	}
	digest, err := helper.RegistryDigest(getenv("UBAG_ADAPTERS_DIR", "adapters"))
	if err != nil {
		return nil, fmt.Errorf("UBAG_HELPER_VOICE=true: adapter registry digest: %w", err)
	}
	secret := []byte(strings.TrimSpace(os.Getenv("UBAG_VOICE_RELAY_SECRET")))
	if len(secret) == 0 {
		slog.Warn("UBAG_VOICE_RELAY_SECRET is not set: no per-attempt credentials can be derived, so helper-hosted voice media is unavailable")
	}
	return voice.NewRemoteNegotiator(voice.RemoteConfig{
		Store:  store,
		Minter: voice.HelperVoiceMinter{Master: secret, ICE: voiceICEConfigFromEnv()},
		Dial: func(ctx context.Context, nodeID, endpoint string) (voice.RemoteConn, error) {
			conn, err := dialer.Dial(ctx, nodeID, endpoint)
			if err != nil {
				return nil, err
			}
			return conn, nil
		},
		Endpoint: func(ctx context.Context, nodeID string) (string, error) {
			alloc, err := nodeStore.GetAllocation(ctx, nodeID)
			if err != nil {
				return "", err
			}
			return alloc.Endpoint, nil
		},
		ProfileRef:      placer.ProfileRef,
		WorkloadVersion: strings.TrimSpace(getenv("UBAG_HELPER_WORKLOAD_VERSION", "")),
		RegistryDigest:  digest,
		OnFenced:        helpermetrics.RecordFencedReject,
		OnRenewFailure:  func(reason string) { helpermetrics.RecordLeaseRenewFailure(helpermetrics.LeaseVoice, reason) },
	})
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
