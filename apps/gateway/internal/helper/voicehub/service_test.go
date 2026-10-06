package voicehub

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// eventStream is the server side of StreamVoiceEvents without gRPC.
type eventStream struct {
	grpc.ServerStream
	ctx    context.Context
	mu     sync.Mutex
	events []*helperv1.VoiceEvent
}

func (s *eventStream) Context() context.Context { return s.ctx }

func (s *eventStream) Send(r *helperv1.StreamVoiceEventsResponse) error {
	s.mu.Lock()
	s.events = append(s.events, r.GetEvent())
	s.mu.Unlock()
	return nil
}

func (s *eventStream) types() []helperv1.VoiceEventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []helperv1.VoiceEventType
	for _, e := range s.events {
		out = append(out, e.GetType())
	}
	return out
}

func (s *eventStream) last() *helperv1.VoiceEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return nil
	}
	return s.events[len(s.events)-1]
}

// The helper endpoint end to end, minus gRPC: the real service core over the
// real voicehub (voice.MediaHub, a real pion client) and a relay that behaves like
// audio-relay.py. A call is answered, audio reaches the relay under the per-call
// key, a renewal keeps it alive, and when renewal stops the NODE ends it by itself:
// media down, the provider's voice UI deactivated through the node's own worker, the
// relay key gone.
func TestVoiceHelperEndpointEndToEndDeadManWithoutRenew(t *testing.T) {
	r := newRig(t)
	var (
		mu   sync.Mutex
		jobs []helper.AttemptSpec
	)
	runner := helper.RunnerFunc(func(_ context.Context, spec helper.AttemptSpec, emit helper.EmitFunc) error {
		mu.Lock()
		jobs = append(jobs, spec)
		mu.Unlock()
		state := "activated"
		if spec.CommandType == "voice.deactivate" {
			state = "deactivated"
		}
		return emit(helper.Event{
			Type: helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL,
			Outcome: &helperv1.AttemptOutcome{
				Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, ResultJson: `{"state":"` + state + `"}`,
			},
		})
	})
	srv, err := helper.NewServer(helper.Config{
		NodeID: "helper-1", WorkloadVersion: "w1", RegistryDigest: strings.Repeat("a", 64),
		Runner: runner, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), LeaseMaxTTL: 2 * time.Second, MaxAttempts: 1,
		Voice: &helper.VoiceConfig{Media: r.hub, Environments: []helper.VoiceEnvironment{r.env}, ActivateTimeout: 5 * time.Second, DeactivateTimeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	svc := srv.VoiceService()

	fence := func(expires time.Duration) *helperv1.VoiceFence {
		return &helperv1.VoiceFence{
			SessionId: tSession, TenantId: "tenant-a", AttemptId: tAttempt, NodeId: "helper-1", LeaseGeneration: 7,
			ExpiresAt: timestamppb.New(time.Now().Add(expires)),
		}
	}
	pc, mic, _, offer := clientOffer(t)
	resp, err := svc.OfferVoice(t.Context(), &helperv1.OfferVoiceRequest{
		Fence:       fence(time.Second),
		Credentials: &helperv1.VoiceCredentials{RelayKey: relayKey, MediaKey: mediaKey},
		SdpOffer:    offer, IdentityRef: "ident-1", InstanceRef: "env-a", Target: "chatgpt_web",
	})
	if err != nil {
		t.Fatalf("OfferVoice: %v", err)
	}
	accept(t, pc, resp.GetSdpAnswer())

	events := &eventStream{ctx: t.Context()}
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- svc.StreamVoiceEvents(&helperv1.StreamVoiceEventsRequest{Fence: fence(time.Minute)}, events)
	}()

	// Audio flows under the per-call relay key, and the node activated the
	// provider's voice UI on its own loopback browser.
	go sendSamples(mic, 400)
	eventually(t, "mic frames at the relay", func() bool { _, f, _, _ := r.relay.snapshot(); return f >= 3 })
	if got, ok := fileContent(r.env.RelayKeyFile); !ok || got != string(relayKey) {
		t.Fatal("the relay was not handed the per-call key")
	}
	eventually(t, "CONNECTED", func() bool {
		for _, ty := range events.types() {
			if ty == helperv1.VoiceEventType_VOICE_EVENT_TYPE_CONNECTED {
				return true
			}
		}
		return false
	})
	mu.Lock()
	if len(jobs) != 1 || jobs[0].CommandType != "voice.activate" || !strings.Contains(jobs[0].InputJSON, `"cdp_endpoint":"http://127.0.0.1:9222"`) {
		t.Fatalf("activation jobs = %+v", jobs)
	}
	mu.Unlock()

	// Renew once, shortly before the first expiry: the call must outlive it.
	time.Sleep(400 * time.Millisecond)
	renewed, err := svc.RenewVoice(t.Context(), &helperv1.RenewVoiceRequest{Fence: fence(time.Minute), ExpiresAt: timestamppb.New(time.Now().Add(2 * time.Second))})
	if err != nil || renewed.GetState() == helperv1.VoiceState_VOICE_STATE_ENDED {
		t.Fatalf("RenewVoice = %v, %v", renewed, err)
	}
	time.Sleep(900 * time.Millisecond) // past the ORIGINAL expiry (1 s)
	if last := events.last(); last == nil || last.GetType() == helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED {
		t.Fatalf("the call ended despite its renewal: %v", events.types())
	}
	if r.hub.Active() != 1 {
		t.Fatal("the media was torn down despite the renewal")
	}

	// No more renewals: the node ends the call itself.
	select {
	case err := <-streamDone:
		if err != nil {
			t.Fatalf("event stream: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the call never ended without renewal")
	}
	end := events.last()
	if end.GetType() != helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED || end.GetEndReason() != "lease_expired" ||
		!strings.Contains(end.GetDataJson(), `"deactivated":true`) {
		t.Fatalf("last event = %v", end)
	}
	if r.hub.Active() != 0 {
		t.Fatal("media outlived the lease")
	}
	if _, ok := fileContent(r.env.RelayKeyFile); ok {
		t.Fatal("the relay key outlived the lease")
	}
	eventually(t, "the relay connection to close", func() bool { _, _, _, c := r.relay.snapshot(); return c == 1 })
	mu.Lock()
	defer mu.Unlock()
	if len(jobs) != 2 || jobs[1].CommandType != "voice.deactivate" {
		t.Fatalf("jobs = %+v, want activate then deactivate", jobs)
	}
}
