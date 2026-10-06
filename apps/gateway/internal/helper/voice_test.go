package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// ---- fakes -------------------------------------------------------------------

// fakeVoiceMedia stands in for the loopback-relay media endpoint: it answers
// offers, records everything the service asks of it and lets a test play the
// sink (peer connected, media lost, client mute).
type fakeVoiceMedia struct {
	mu       sync.Mutex
	opened   []VoiceCall
	reoffers []string
	sinks    map[string]VoiceSink // by session id
	sessions map[string]string    // call key -> session id
	muteOps  []bool
	closes   map[string]int // by call key
	openErr  error
	// openGate, when set, blocks Open until it is closed (or ctx ends, unless
	// openIgnoresCtx: a media endpoint that completes the offer regardless).
	openGate       chan struct{}
	openStarted    chan struct{}
	openIgnoresCtx bool
}

func newFakeVoiceMedia() *fakeVoiceMedia {
	return &fakeVoiceMedia{sinks: map[string]VoiceSink{}, sessions: map[string]string{}, closes: map[string]int{}}
}

func (f *fakeVoiceMedia) Open(ctx context.Context, call VoiceCall, sdp string, sink VoiceSink) (string, error) {
	f.mu.Lock()
	f.opened = append(f.opened, call)
	gate, started, err, ignoreCtx := f.openGate, f.openStarted, f.openErr, f.openIgnoresCtx
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if gate != nil {
		if ignoreCtx {
			<-gate
		} else {
			select {
			case <-gate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	f.sinks[call.SessionID] = sink
	f.sessions[call.Key()] = call.SessionID
	f.mu.Unlock()
	return "answer:" + sdp, nil
}

func (f *fakeVoiceMedia) Reoffer(_ context.Context, key, sdp string, muted bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reoffers = append(f.reoffers, fmt.Sprintf("%s|%s|muted=%v", key, sdp, muted))
	return "reanswer:" + sdp, nil
}

func (f *fakeVoiceMedia) SetMuted(_ string, muted bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.muteOps = append(f.muteOps, muted)
	return true
}

func (f *fakeVoiceMedia) Close(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes[key]++
}

// closeCount is how often the media was closed for any call of the session.
func (f *fakeVoiceMedia) closeCount(sessionID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for key, c := range f.closes {
		// A Close can arrive for a call whose Open never completed: the key then
		// has no session recorded, and its attempt prefix is the only link.
		if f.sessions[key] == sessionID || (f.sessions[key] == "" && strings.HasPrefix(key, vAttempt+"#")) {
			n += c
		}
	}
	return n
}

func (f *fakeVoiceMedia) sink(sessionID string) VoiceSink {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sinks[sessionID]
}

func (f *fakeVoiceMedia) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opened)
}

// voiceScript plays the worker for voice.activate / voice.deactivate. A nil
// outcome function defaults to success.
func voiceScript(activate, deactivate func() *helperv1.AttemptOutcome) func(context.Context, AttemptSpec, EmitFunc) error {
	return func(ctx context.Context, spec AttemptSpec, emit EmitFunc) error {
		if err := emit(ev(evStarted, "")); err != nil {
			return err
		}
		pick := activate
		state := "activated"
		if spec.CommandType == "voice.deactivate" {
			pick, state = deactivate, "deactivated"
		}
		if pick != nil {
			return emit(terminal(pick()))
		}
		return emit(terminal(&helperv1.AttemptOutcome{
			Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, ResultJson: `{"state":"` + state + `"}`,
		}))
	}
}

func voiceCfg(m *fakeVoiceMedia) func(*Config) {
	return func(c *Config) {
		c.Voice = &VoiceConfig{
			Media: m,
			Environments: []VoiceEnvironment{
				{ID: "env-a", CDPEndpoint: "http://127.0.0.1:9222", RelayAddr: "127.0.0.1:9099", RelayKeyFile: "/run/ubag/relay-key.9099"},
				{ID: "env-b", CDPEndpoint: "http://127.0.0.1:9223", RelayAddr: "127.0.0.1:9100", RelayKeyFile: "/run/ubag/relay-key.9100"},
			},
			ActivateTimeout: 5 * time.Second, DeactivateTimeout: 5 * time.Second,
		}
	}
}

func newVoiceRig(t *testing.T, runner *fakeRunner, m *fakeVoiceMedia) *rig {
	t.Helper()
	return newRig(t, runner, voiceCfg(m))
}

const (
	vSession = "voice_1700000000_aabbccdd"
	vAttempt = "att_voice1"
	vTenant  = "tenant-a"
)

var (
	relayKeyBytes = []byte(strings.Repeat("a1", 32))
	mediaKeyBytes = []byte(strings.Repeat("b2", 32))
)

func (r *rig) vfence(session, attempt string, gen uint64) *helperv1.VoiceFence {
	return &helperv1.VoiceFence{
		SessionId: session, TenantId: vTenant, AttemptId: attempt, NodeId: "helper-1", LeaseGeneration: gen,
		ExpiresAt: timestamppb.New(r.clock.Now().Add(120 * time.Second)),
	}
}

func (r *rig) offerReq(session, attempt string, gen uint64) *helperv1.OfferVoiceRequest {
	return &helperv1.OfferVoiceRequest{
		Fence: r.vfence(session, attempt, gen),
		Credentials: &helperv1.VoiceCredentials{
			RelayKey: relayKeyBytes, MediaKey: mediaKeyBytes,
			IceServers: []*helperv1.VoiceIceServer{
				{Urls: []string{"stun:stun.example:3478"}},
				{Urls: []string{"turn:turn.example:3478"}, Username: "1700000000:s:att_voice1", Credential: "minted-by-primary"},
			},
		},
		SdpOffer: "v=0 client-offer", IdentityRef: "ident-v1", InstanceRef: "env-a", Target: "chatgpt_web", TraceId: "trace-v1",
	}
}

func (r *rig) offer(req *helperv1.OfferVoiceRequest) (*helperv1.OfferVoiceResponse, error) {
	r.t.Helper()
	return r.voice.OfferVoice(r.ctx(), req)
}

func (r *rig) mustOffer(session, attempt string, gen uint64) *helperv1.OfferVoiceResponse {
	r.t.Helper()
	resp, err := r.offer(r.offerReq(session, attempt, gen))
	if err != nil {
		r.t.Fatalf("OfferVoice: %v", err)
	}
	return resp
}

func (r *rig) control(session, attempt string, gen uint64, op helperv1.VoiceControlOp) (*helperv1.ControlVoiceResponse, error) {
	r.t.Helper()
	return r.voice.ControlVoice(r.ctx(), &helperv1.ControlVoiceRequest{Fence: r.vfence(session, attempt, gen), Op: op})
}

func (r *rig) vcall(session string) *voiceCall {
	r.t.Helper()
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	c := r.srv.voiceCalls[session]
	if c == nil {
		r.t.Fatalf("no voice call %s", session)
	}
	return c
}

func (r *rig) vevents(session string) []*helperv1.VoiceEvent {
	c := r.vcall(session)
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*helperv1.VoiceEvent(nil), c.events...)
}

func (r *rig) vtypes(session string) []helperv1.VoiceEventType {
	var out []helperv1.VoiceEventType
	for _, e := range r.vevents(session) {
		out = append(out, e.GetType())
	}
	return out
}

// waitVoiceEnded waits until the call released everything and wrote ENDED.
func (r *rig) waitVoiceEnded(session string) *helperv1.VoiceEvent {
	r.t.Helper()
	c := r.vcall(session)
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
		r.t.Fatalf("voice call %s never ended", session)
	}
	evs := r.vevents(session)
	last := evs[len(evs)-1]
	if last.GetType() != vtEnded {
		r.t.Fatalf("last event is %v, want ENDED", last.GetType())
	}
	return last
}

const (
	vtStarted      = helperv1.VoiceEventType_VOICE_EVENT_TYPE_STARTED
	vtConnected    = helperv1.VoiceEventType_VOICE_EVENT_TYPE_CONNECTED
	vtMuted        = helperv1.VoiceEventType_VOICE_EVENT_TYPE_MUTED
	vtUnmuted      = helperv1.VoiceEventType_VOICE_EVENT_TYPE_UNMUTED
	vtReconnecting = helperv1.VoiceEventType_VOICE_EVENT_TYPE_RECONNECTING
	vtWarning      = helperv1.VoiceEventType_VOICE_EVENT_TYPE_WARNING
	vtEnded        = helperv1.VoiceEventType_VOICE_EVENT_TYPE_ENDED

	opMute      = helperv1.VoiceControlOp_VOICE_CONTROL_OP_MUTE
	opUnmute    = helperv1.VoiceControlOp_VOICE_CONTROL_OP_UNMUTE
	opInterrupt = helperv1.VoiceControlOp_VOICE_CONTROL_OP_INTERRUPT
	opTerminate = helperv1.VoiceControlOp_VOICE_CONTROL_OP_TERMINATE
)

func dataOf(t *testing.T, e *helperv1.VoiceEvent) map[string]any {
	t.Helper()
	out := map[string]any{}
	if e.GetDataJson() != "" {
		if err := json.Unmarshal([]byte(e.GetDataJson()), &out); err != nil {
			t.Fatalf("event data %q: %v", e.GetDataJson(), err)
		}
	}
	return out
}

func activateSpecs(r *fakeRunner, command string) []AttemptSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []AttemptSpec
	for _, s := range r.specs {
		if s.CommandType == command {
			out = append(out, s)
		}
	}
	return out
}

// ---- the exit gate: offer, dead-man, stale generation, no TURN secret --------

func TestVoiceOfferReturnsAnswerAndActivatesProviderLocally(t *testing.T) {
	m := newFakeVoiceMedia()
	runner := newFakeRunner(voiceScript(nil, nil))
	r := newVoiceRig(t, runner, m)

	resp := r.mustOffer(vSession, vAttempt, 3)
	if resp.GetSdpAnswer() != "answer:v=0 client-offer" || !resp.GetRelayReady() {
		t.Fatalf("response = %v", resp)
	}
	if m.openCount() != 1 {
		t.Fatalf("media opened %d times", m.openCount())
	}
	call := m.opened[0]
	if call.Env.ID != "env-a" || call.Env.RelayAddr != "127.0.0.1:9099" || call.Generation != 3 || call.NodeID != "helper-1" {
		t.Fatalf("media got the wrong environment or fence: %+v", call)
	}

	// The provider voice UI is activated on THIS node's own loopback browser.
	eventually(t, "voice.activate", func() bool { return len(activateSpecs(runner, "voice.activate")) == 1 })
	spec := activateSpecs(runner, "voice.activate")[0]
	var in map[string]any
	if err := json.Unmarshal([]byte(spec.InputJSON), &in); err != nil {
		t.Fatal(err)
	}
	if in["cdp_endpoint"] != "http://127.0.0.1:9222" || in["provider_id"] != "chatgpt_web" || in["action"] != "activate" ||
		spec.Target != "chatgpt_web" || spec.IdentityRef != "ident-v1" || spec.AttemptID != vAttempt {
		t.Fatalf("activation job = %+v input=%v", spec, in)
	}

	// CONNECTED needs the provider ready AND the client's peer up, in any order.
	eventually(t, "provider ready", func() bool {
		c := r.vcall(vSession)
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.providerReady
	})
	if got := r.vtypes(vSession); len(got) != 1 || got[0] != vtStarted {
		t.Fatalf("events before the peer is up = %v", got)
	}
	m.sink(vSession).PeerConnected()
	if got := r.vtypes(vSession); len(got) != 2 || got[1] != vtConnected {
		t.Fatalf("events after the peer is up = %v", got)
	}
	if st, err := r.control(vSession, vAttempt, 3, opUnmute); err != nil || st.GetState() != helperv1.VoiceState_VOICE_STATE_CONNECTED {
		t.Fatalf("state = %v, %v", st, err)
	}
	if got := r.capacity().GetAttemptsActive(); got != 1 {
		t.Fatalf("a call counts as one node workload, capacity says %d", got)
	}
}

func TestVoiceDeadManEndsTheCallWithoutRenew(t *testing.T) {
	m := newFakeVoiceMedia()
	runner := newFakeRunner(voiceScript(nil, nil))
	r := newVoiceRig(t, runner, m)
	r.mustOffer(vSession, vAttempt, 1)
	eventually(t, "voice.activate", func() bool { return len(activateSpecs(runner, "voice.activate")) == 1 })

	r.clock.Advance(119 * time.Second)
	if !r.vcall(vSession).live() || m.closeCount(vSession) != 0 {
		t.Fatal("the call ended before its lease did")
	}
	// No renew: the node ends the call by itself, the media first.
	r.clock.Advance(2 * time.Second)
	end := r.waitVoiceEnded(vSession)
	if end.GetEndReason() != "lease_expired" {
		t.Fatalf("end_reason = %q, want lease_expired", end.GetEndReason())
	}
	if m.closeCount(vSession) == 0 {
		t.Fatal("the media was not closed at lease expiry")
	}
	if data := dataOf(t, end); data["deactivated"] != true {
		t.Fatalf("provider voice was not deactivated before ENDED: %v", data)
	}
	if len(activateSpecs(runner, "voice.deactivate")) != 1 {
		t.Fatal("no voice.deactivate ran")
	}
	// Environment and identity are free again.
	if got := r.capacity().GetAttemptsActive(); got != 0 {
		t.Fatalf("capacity still held: %d", got)
	}
	r.mustOffer("voice_1700000001_ddeeff", "att_voice2", 1)
}

func TestVoiceRenewKeepsTheCallAliveUntilRenewalStops(t *testing.T) {
	m := newFakeVoiceMedia()
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	r.mustOffer(vSession, vAttempt, 1)
	for i := 0; i < 4; i++ {
		r.clock.Advance(60 * time.Second)
		resp, err := r.voice.RenewVoice(r.ctx(), &helperv1.RenewVoiceRequest{
			Fence: r.vfence(vSession, vAttempt, 1), ExpiresAt: timestamppb.New(r.clock.Now().Add(120 * time.Second)),
		})
		if err != nil || resp.GetLeaseGeneration() != 1 || resp.GetState() == helperv1.VoiceState_VOICE_STATE_ENDED {
			t.Fatalf("renew %d = %v, %v", i, resp, err)
		}
	}
	if !r.vcall(vSession).live() {
		t.Fatal("a renewed call must not be ended")
	}
	r.clock.Advance(130 * time.Second)
	if r.waitVoiceEnded(vSession).GetEndReason() != "lease_expired" {
		t.Fatal("the call must end once renewal stops")
	}
	// A late renew never resurrects it.
	resp, err := r.voice.RenewVoice(r.ctx(), &helperv1.RenewVoiceRequest{
		Fence: r.vfence(vSession, vAttempt, 1), ExpiresAt: timestamppb.New(r.clock.Now().Add(120 * time.Second)),
	})
	if err != nil || resp.GetState() != helperv1.VoiceState_VOICE_STATE_ENDED {
		t.Fatalf("late renew = %v, %v", resp, err)
	}
}

func TestVoiceRenewIsClampedAndNeverShortens(t *testing.T) {
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), newFakeVoiceMedia())
	r.mustOffer(vSession, vAttempt, 1)
	renew := func(d time.Duration) time.Time {
		resp, err := r.voice.RenewVoice(r.ctx(), &helperv1.RenewVoiceRequest{
			Fence: r.vfence(vSession, vAttempt, 1), ExpiresAt: timestamppb.New(r.clock.Now().Add(d)),
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp.GetExpiresAt().AsTime()
	}
	if got := renew(24 * time.Hour); got.Sub(r.clock.Now()) > 5*time.Minute+time.Second {
		t.Fatalf("a wrong clock parked the call for %v", got.Sub(r.clock.Now()))
	}
	before := renew(24 * time.Hour)
	if after := renew(time.Second); !after.Equal(before) {
		t.Fatalf("renew shortened the lease: %v -> %v", before, after)
	}
	if _, err := r.voice.RenewVoice(r.ctx(), &helperv1.RenewVoiceRequest{Fence: r.vfence(vSession, vAttempt, 1)}); codeOf(err) != "InvalidArgument" {
		t.Fatalf("renew without expires_at = %v", err)
	}
}

func TestVoiceStaleGenerationControlIsRejected(t *testing.T) {
	m := newFakeVoiceMedia()
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	r.mustOffer(vSession, vAttempt, 3)

	for _, c := range []struct {
		name    string
		attempt string
		gen     uint64
	}{
		{"older generation", vAttempt, 2},
		{"forged future generation", vAttempt, 4},
		{"another attempt, same generation", "att_other", 3},
	} {
		for _, op := range []helperv1.VoiceControlOp{opMute, opUnmute, opTerminate} {
			if _, err := r.control(vSession, c.attempt, c.gen, op); codeOf(err) != "Aborted" {
				t.Errorf("%s %v = %v, want Aborted", c.name, op, codeOf(err))
			}
		}
		if _, err := r.voice.RenewVoice(r.ctx(), &helperv1.RenewVoiceRequest{
			Fence: r.vfence(vSession, c.attempt, c.gen), ExpiresAt: timestamppb.New(r.clock.Now().Add(time.Minute)),
		}); codeOf(err) != "Aborted" {
			t.Errorf("%s renew = %v, want Aborted", c.name, codeOf(err))
		}
		if _, err := r.voice.ReconnectVoice(r.ctx(), &helperv1.ReconnectVoiceRequest{Fence: r.vfence(vSession, c.attempt, c.gen), SdpOffer: "v=0"}); codeOf(err) != "Aborted" {
			t.Errorf("%s reconnect = %v, want Aborted", c.name, codeOf(err))
		}
	}
	if !r.vcall(vSession).live() || len(m.muteOps) != 0 || len(m.reoffers) != 0 || m.closeCount(vSession) != 0 {
		t.Fatalf("a stale writer reached the live call: muteOps=%v reoffers=%v closes=%d", m.muteOps, m.reoffers, m.closeCount(vSession))
	}
	// The exact fence still works.
	if st, err := r.control(vSession, vAttempt, 3, opMute); err != nil || !st.GetMuted() {
		t.Fatalf("current fence mute = %v, %v", st, err)
	}
}

func TestVoiceHelperHoldsNoTURNSecret(t *testing.T) {
	// 1. The wire contract: the credentials message carries per-attempt material
	//    only, and no field of any helper voice message is a secret.
	fields := (&helperv1.VoiceCredentials{}).ProtoReflect().Descriptor().Fields()
	var names []string
	for i := 0; i < fields.Len(); i++ {
		names = append(names, string(fields.Get(i).Name()))
	}
	if !reflect.DeepEqual(names, []string{"relay_key", "media_key", "ice_servers"}) {
		t.Fatalf("VoiceCredentials fields = %v", names)
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(VoiceCall{}), reflect.TypeOf(VoiceConfig{}), reflect.TypeOf(VoiceEnvironment{}), reflect.TypeOf(VoiceICEServer{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			if strings.Contains(strings.ToLower(typ.Field(i).Name), "secret") {
				t.Errorf("%s.%s: the helper's voice configuration must have no secret field", typ.Name(), typ.Field(i).Name)
			}
		}
	}
	// 2. The process refuses to start with the global secrets in its environment.
	for _, name := range []string{"UBAG_VOICE_TURN_SECRET", "UBAG_VOICE_RELAY_SECRET", "UBAG_APP_SECRET"} {
		if err := CheckEnvIsolation([]string{name + "=x"}); err == nil {
			t.Errorf("%s must be refused on a Helper Node", name)
		}
	}
	// 3. A call carries exactly what the primary minted for it, nothing is derived
	//    locally, and the service keeps no copy after handing it to the media.
	m := newFakeVoiceMedia()
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	r.mustOffer(vSession, vAttempt, 1)
	got := m.opened[0].Credentials
	if string(got.RelayKey) != string(relayKeyBytes) || string(got.MediaKey) != string(mediaKeyBytes) ||
		len(got.ICEServers) != 2 || got.ICEServers[1].Credential != "minted-by-primary" || got.ICEServers[0].Username != "" {
		t.Fatalf("media got %+v", got)
	}
	c := r.vcall(vSession)
	if !reflect.DeepEqual(c.info.Credentials, VoiceCredentials{}) {
		t.Fatal("the service retained the call's credentials")
	}
	// 4. They never print.
	for _, s := range []string{fmt.Sprintf("%v", got), fmt.Sprintf("%+v", got), fmt.Sprintf("%#v", got), fmt.Sprint(m.opened[0])} {
		if strings.Contains(s, string(relayKeyBytes)) || strings.Contains(s, string(mediaKeyBytes)) || strings.Contains(s, "minted-by-primary") {
			t.Fatalf("credentials leaked into a formatted value: %s", s)
		}
	}
}

// ---- admission ---------------------------------------------------------------

func TestVoiceOfferValidation(t *testing.T) {
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), newFakeVoiceMedia())
	for _, c := range []struct {
		name string
		mut  func(*helperv1.OfferVoiceRequest)
		want string
	}{
		{"another node", func(q *helperv1.OfferVoiceRequest) { q.Fence.NodeId = "helper-2" }, "PermissionDenied"},
		{"no fence", func(q *helperv1.OfferVoiceRequest) { q.Fence = nil }, "InvalidArgument"},
		{"generation zero", func(q *helperv1.OfferVoiceRequest) { q.Fence.LeaseGeneration = 0 }, "InvalidArgument"},
		{"generation past the JSON-safe bound", func(q *helperv1.OfferVoiceRequest) { q.Fence.LeaseGeneration = 1 << 54 }, "InvalidArgument"},
		{"bad attempt id", func(q *helperv1.OfferVoiceRequest) { q.Fence.AttemptId = "x" }, "InvalidArgument"},
		{"bad session id", func(q *helperv1.OfferVoiceRequest) { q.Fence.SessionId = "a|b" }, "InvalidArgument"},
		{"expired fence", func(q *helperv1.OfferVoiceRequest) { q.Fence.ExpiresAt = timestamppb.New(time.Now().Add(-time.Hour)) }, "DeadlineExceeded"},
		{"no expiry", func(q *helperv1.OfferVoiceRequest) { q.Fence.ExpiresAt = nil }, "InvalidArgument"},
		{"short relay key", func(q *helperv1.OfferVoiceRequest) { q.Credentials.RelayKey = []byte("short") }, "InvalidArgument"},
		{"no media key", func(q *helperv1.OfferVoiceRequest) { q.Credentials.MediaKey = nil }, "InvalidArgument"},
		{"relay key equal to media key", func(q *helperv1.OfferVoiceRequest) { q.Credentials.MediaKey = q.Credentials.RelayKey }, "InvalidArgument"},
		{"no credentials", func(q *helperv1.OfferVoiceRequest) { q.Credentials = nil }, "InvalidArgument"},
		{"TURN without the minted credential", func(q *helperv1.OfferVoiceRequest) { q.Credentials.IceServers[1].Credential = "" }, "InvalidArgument"},
		{"not an ICE URL", func(q *helperv1.OfferVoiceRequest) { q.Credentials.IceServers[0].Urls = []string{"http://x"} }, "InvalidArgument"},
		{"too many ICE servers", func(q *helperv1.OfferVoiceRequest) {
			for i := 0; i < 9; i++ {
				q.Credentials.IceServers = append(q.Credentials.IceServers, &helperv1.VoiceIceServer{Urls: []string{"stun:s:1"}})
			}
		}, "InvalidArgument"},
		{"empty offer", func(q *helperv1.OfferVoiceRequest) { q.SdpOffer = "" }, "InvalidArgument"},
		{"oversized offer", func(q *helperv1.OfferVoiceRequest) { q.SdpOffer = strings.Repeat("x", maxVoiceOfferBytes+1) }, "InvalidArgument"},
		{"antigravity target", func(q *helperv1.OfferVoiceRequest) { q.Target = "antigravity_chat" }, "InvalidArgument"},
		{"bad identity", func(q *helperv1.OfferVoiceRequest) { q.IdentityRef = "../x" }, "InvalidArgument"},
		{"no instance", func(q *helperv1.OfferVoiceRequest) { q.InstanceRef = "" }, "InvalidArgument"},
		{"an environment this node does not have", func(q *helperv1.OfferVoiceRequest) { q.InstanceRef = "env-z" }, "FailedPrecondition"},
	} {
		req := r.offerReq(vSession, vAttempt, 1)
		c.mut(req)
		if _, err := r.offer(req); codeOf(err) != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
	r.srv.mu.Lock()
	n := len(r.srv.voiceCalls) + len(r.srv.voiceEnvBusy)
	r.srv.mu.Unlock()
	if n != 0 || r.capacity().GetAttemptsActive() != 0 {
		t.Fatalf("a refused offer left state behind (calls=%d)", n)
	}
}

func TestVoiceOneExclusiveEnvironmentPerCall(t *testing.T) {
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), newFakeVoiceMedia())
	r.mustOffer(vSession, vAttempt, 1)

	// The same environment is taken, whoever asks.
	other := r.offerReq("voice_1700000002_x", "att_voice2", 1)
	other.IdentityRef = "ident-v2"
	if _, err := r.offer(other); codeOf(err) != "Unavailable" {
		t.Fatalf("second call on the same environment = %v, want Unavailable", err)
	}
	// The same provider identity is taken even on another environment (D6).
	same := r.offerReq("voice_1700000003_y", "att_voice3", 1)
	same.InstanceRef = "env-b"
	if _, err := r.offer(same); codeOf(err) != "Unavailable" {
		t.Fatalf("same identity on another environment = %v, want Unavailable", err)
	}
	// Another identity on the other environment is fine.
	ok := r.offerReq("voice_1700000004_z", "att_voice4", 1)
	ok.InstanceRef, ok.IdentityRef = "env-b", "ident-v2"
	if _, err := r.offer(ok); err != nil {
		t.Fatalf("a free environment and identity: %v", err)
	}
	// Both are busy, and the node is at its attempt capacity (2).
	third := r.offerReq("voice_1700000005_w", "att_voice5", 1)
	third.IdentityRef = "ident-v3"
	if _, err := r.offer(third); codeOf(err) != "Unavailable" {
		t.Fatalf("over capacity = %v, want Unavailable", err)
	}
	// A text attempt on a busy identity is refused too: voice excludes jobs.
	runner := r.runReq("att_text1", 1)
	runner.Provider, runner.Target, runner.IdentityRef = "chatgpt_web", "chatgpt_web", "ident-v1"
	rs := r.open(runner)
	if _, err := rs.recv(); codeOf(err) != "Unavailable" {
		t.Fatalf("RunAttempt on a voice identity = %v, want Unavailable", err)
	}
}

func TestVoiceRepeatedOfferRenegotiatesAndNeverActivatesTwice(t *testing.T) {
	m := newFakeVoiceMedia()
	runner := newFakeRunner(voiceScript(nil, nil))
	r := newVoiceRig(t, runner, m)
	r.mustOffer(vSession, vAttempt, 1)
	eventually(t, "voice.activate", func() bool { return len(activateSpecs(runner, "voice.activate")) == 1 })

	req := r.offerReq(vSession, vAttempt, 1)
	req.SdpOffer = "v=0 retry"
	resp, err := r.offer(req)
	if err != nil || resp.GetSdpAnswer() != "reanswer:v=0 retry" {
		t.Fatalf("retried OfferVoice = %v, %v", resp, err)
	}
	if m.openCount() != 1 || len(m.reoffers) != 1 || len(activateSpecs(runner, "voice.activate")) != 1 {
		t.Fatalf("opens=%d reoffers=%d activations=%d", m.openCount(), len(m.reoffers), len(activateSpecs(runner, "voice.activate")))
	}
	// A different environment or identity under the same fence is not the same call.
	bad := r.offerReq(vSession, vAttempt, 1)
	bad.IdentityRef = "ident-other"
	if _, err := r.offer(bad); codeOf(err) != "FailedPrecondition" {
		t.Fatalf("mismatching repeat = %v", err)
	}
}

func TestVoiceNewerGenerationSupersedesAndLowerOrEqualIsStale(t *testing.T) {
	m := newFakeVoiceMedia()
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	r.mustOffer(vSession, vAttempt, 3)

	for _, c := range []struct {
		attempt string
		gen     uint64
	}{{"att_voice9", 3}, {"att_voice9", 2}} {
		if _, err := r.offer(r.offerReq(vSession, c.attempt, c.gen)); codeOf(err) != "Aborted" {
			t.Fatalf("offer %+v = %v, want Aborted", c, err)
		}
	}
	if !r.vcall(vSession).live() {
		t.Fatal("a stale offer ended the live call")
	}
	// A newer generation fences the older holder out; the new call is admitted
	// once the old one released the environment (the first try is Unavailable).
	next := r.offerReq(vSession, "att_voice9", 4)
	_, err := r.offer(next)
	if err != nil && codeOf(err) != "Unavailable" {
		t.Fatalf("first offer of the newer generation = %v", err)
	}
	eventually(t, "the newer generation to be admitted", func() bool {
		_, err := r.offer(next)
		return err == nil
	})
	if got := r.vcall(vSession).info.Generation; got != 4 {
		t.Fatalf("the session is held at generation %d", got)
	}
	if _, err := r.control(vSession, vAttempt, 3, opMute); codeOf(err) != "Aborted" {
		t.Fatalf("the superseded generation must be fenced out, got %v", err)
	}
}

func TestVoiceFailedOfferLeavesNoStateAndRetryStartsClean(t *testing.T) {
	m := newFakeVoiceMedia()
	m.openErr = ErrVoiceRelayUnavailable
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	if _, err := r.offer(r.offerReq(vSession, vAttempt, 1)); codeOf(err) != "Unavailable" {
		t.Fatalf("relay down = %v, want Unavailable", err)
	}
	if m.closeCount(vSession) == 0 {
		t.Fatal("a failed offer must close whatever the media half-opened")
	}
	eventually(t, "the failed call to be forgotten", func() bool {
		r.srv.mu.Lock()
		defer r.srv.mu.Unlock()
		return len(r.srv.voiceCalls) == 0 && len(r.srv.voiceEnvBusy) == 0
	})
	if got := r.capacity().GetAttemptsActive(); got != 0 {
		t.Fatalf("capacity leaked: %d", got)
	}
	m.mu.Lock()
	m.openErr = nil
	m.mu.Unlock()
	r.mustOffer(vSession, vAttempt, 1) // same fence, now healthy
}

func TestVoiceMediaErrorTextNeverReachesThePrimary(t *testing.T) {
	m := newFakeVoiceMedia()
	m.openErr = errors.New("pion: remote description: v=0 secret-sdp-detail 10.0.0.7")
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	_, err := r.offer(r.offerReq(vSession, vAttempt, 1))
	if codeOf(err) != "Internal" || strings.Contains(err.Error(), "secret-sdp-detail") || strings.Contains(err.Error(), "10.0.0.7") {
		t.Fatalf("err = %v", err)
	}
}

func TestVoiceTerminateWhileTheOfferIsInFlightLeavesNoMedia(t *testing.T) {
	m := newFakeVoiceMedia()
	m.openGate, m.openStarted, m.openIgnoresCtx = make(chan struct{}), make(chan struct{}), true
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	type result struct {
		resp *helperv1.OfferVoiceResponse
		err  error
	}
	done := make(chan result, 1)
	go func() { resp, err := r.offer(r.offerReq(vSession, vAttempt, 1)); done <- result{resp, err} }()
	<-m.openStarted
	if st, err := r.control(vSession, vAttempt, 1, opTerminate); err != nil || st.GetState() != helperv1.VoiceState_VOICE_STATE_ENDED {
		t.Fatalf("terminate during the offer = %v, %v", st, err)
	}
	close(m.openGate) // Open now completes AFTER the terminate closed an empty hub
	res := <-done
	if res.err == nil {
		t.Fatalf("an offer that was terminated must not succeed: %v", res.resp)
	}
	if m.closeCount(vSession) < 2 {
		t.Fatalf("media must be closed again after a late Open (closes=%d): audio would run on a dead call", m.closeCount(vSession))
	}
	eventually(t, "release", func() bool { return r.capacity().GetAttemptsActive() == 0 })
}

// ---- control, reconnect, events ------------------------------------------------

func TestVoiceMuteUnmuteInterruptAndTerminate(t *testing.T) {
	m := newFakeVoiceMedia()
	runner := newFakeRunner(voiceScript(nil, nil))
	r := newVoiceRig(t, runner, m)
	r.mustOffer(vSession, vAttempt, 2)

	if st, err := r.control(vSession, vAttempt, 2, opMute); err != nil || !st.GetMuted() {
		t.Fatalf("mute = %v, %v", st, err)
	}
	if _, err := r.control(vSession, vAttempt, 2, opMute); err != nil { // idempotent: no second event
		t.Fatal(err)
	}
	if st, err := r.control(vSession, vAttempt, 2, opUnmute); err != nil || st.GetMuted() {
		t.Fatalf("unmute = %v, %v", st, err)
	}
	if got := fmt.Sprint(m.muteOps); got != "[true true false]" {
		t.Fatalf("media saw mute ops %s", got)
	}
	if got := r.vtypes(vSession); fmt.Sprint(got) != fmt.Sprint([]helperv1.VoiceEventType{vtStarted, vtMuted, vtUnmuted}) {
		t.Fatalf("events = %v", got)
	}
	// The client's own mute (data channel) is reported the same way.
	m.sink(vSession).ClientMuted(true)
	if got := r.vtypes(vSession); got[len(got)-1] != vtMuted {
		t.Fatalf("client mute not reported: %v", got)
	}

	// Interrupt has no provider-side primitive: refused, not acknowledged.
	if _, err := r.control(vSession, vAttempt, 2, opInterrupt); codeOf(err) != "Unimplemented" {
		t.Fatalf("interrupt = %v, want Unimplemented", err)
	}
	if _, err := r.control(vSession, vAttempt, 2, helperv1.VoiceControlOp_VOICE_CONTROL_OP_UNSPECIFIED); codeOf(err) != "InvalidArgument" {
		t.Fatalf("no op = %v", err)
	}

	// Terminate: audio is down before the RPC returns; ENDED follows the deactivation.
	st, err := r.control(vSession, vAttempt, 2, opTerminate)
	if err != nil || st.GetState() != helperv1.VoiceState_VOICE_STATE_ENDED {
		t.Fatalf("terminate = %v, %v", st, err)
	}
	if m.closeCount(vSession) == 0 {
		t.Fatal("terminate returned with the media still up")
	}
	end := r.waitVoiceEnded(vSession)
	if end.GetEndReason() != "terminated" || dataOf(t, end)["deactivated"] != true {
		t.Fatalf("ENDED = %v", end)
	}
	// Idempotent, and nothing else works on an ended call.
	if st, err := r.control(vSession, vAttempt, 2, opTerminate); err != nil || st.GetState() != helperv1.VoiceState_VOICE_STATE_ENDED {
		t.Fatalf("second terminate = %v, %v", st, err)
	}
	if _, err := r.voice.ReconnectVoice(r.ctx(), &helperv1.ReconnectVoiceRequest{Fence: r.vfence(vSession, vAttempt, 2), SdpOffer: "v=0"}); codeOf(err) != "FailedPrecondition" {
		t.Fatalf("reconnect after end = %v", err)
	}
	if len(activateSpecs(runner, "voice.deactivate")) != 1 {
		t.Fatalf("deactivated %d times", len(activateSpecs(runner, "voice.deactivate")))
	}
}

func TestVoiceUnknownSessionAndOtherTenantAreIndistinguishable(t *testing.T) {
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), newFakeVoiceMedia())
	r.mustOffer(vSession, vAttempt, 1)

	// Nothing to end: terminate reads as UNKNOWN (the primary reconciles); anything
	// else is NotFound.
	ghost := "voice_1700000009_ghost"
	if st, err := r.control(ghost, vAttempt, 1, opTerminate); err != nil || st.GetState() != helperv1.VoiceState_VOICE_STATE_UNKNOWN {
		t.Fatalf("terminate of an unknown session = %v, %v", st, err)
	}
	if _, err := r.control(ghost, vAttempt, 1, opMute); codeOf(err) != "NotFound" {
		t.Fatalf("mute of an unknown session = %v", err)
	}
	// Another tenant's fence for a real session is the same NotFound, and it
	// neither mutes nor ends it.
	foreign := r.vfence(vSession, vAttempt, 1)
	foreign.TenantId = "tenant-b"
	for _, op := range []helperv1.VoiceControlOp{opMute, opTerminate} {
		st, err := r.voice.ControlVoice(r.ctx(), &helperv1.ControlVoiceRequest{Fence: foreign, Op: op})
		if op == opTerminate {
			if err != nil || st.GetState() != helperv1.VoiceState_VOICE_STATE_UNKNOWN {
				t.Errorf("cross-tenant terminate = %v, %v", st, err)
			}
		} else if codeOf(err) != "NotFound" {
			t.Errorf("cross-tenant %v = %v, want NotFound", op, err)
		}
	}
	if !r.vcall(vSession).live() {
		t.Fatal("another tenant ended the call")
	}
	if _, err := r.voice.ReconnectVoice(r.ctx(), &helperv1.ReconnectVoiceRequest{Fence: foreign, SdpOffer: "v=0"}); codeOf(err) != "NotFound" {
		t.Fatalf("cross-tenant reconnect = %v", err)
	}
	if _, err := r.voice.RenewVoice(r.ctx(), &helperv1.RenewVoiceRequest{Fence: foreign, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}); codeOf(err) != "NotFound" {
		t.Fatalf("cross-tenant renew = %v", err)
	}
	if _, err := r.offer(func() *helperv1.OfferVoiceRequest {
		q := r.offerReq(vSession, vAttempt, 1)
		q.Fence.TenantId = "tenant-b"
		return q
	}()); codeOf(err) != "Aborted" {
		t.Fatalf("cross-tenant offer on a live session = %v, want Aborted", err)
	}
}

func TestVoiceReconnectKeepsProviderVoiceAndCarriesMute(t *testing.T) {
	m := newFakeVoiceMedia()
	runner := newFakeRunner(voiceScript(nil, nil))
	r := newVoiceRig(t, runner, m)
	r.mustOffer(vSession, vAttempt, 1)
	eventually(t, "provider ready", func() bool {
		c := r.vcall(vSession)
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.providerReady
	})
	m.sink(vSession).PeerConnected()
	if _, err := r.control(vSession, vAttempt, 1, opMute); err != nil {
		t.Fatal(err)
	}

	resp, err := r.voice.ReconnectVoice(r.ctx(), &helperv1.ReconnectVoiceRequest{Fence: r.vfence(vSession, vAttempt, 1), SdpOffer: "v=0 ice-restart"})
	if err != nil || resp.GetSdpAnswer() != "reanswer:v=0 ice-restart" {
		t.Fatalf("reconnect = %v, %v", resp, err)
	}
	if len(m.reoffers) != 1 || !strings.HasSuffix(m.reoffers[0], "muted=true") {
		t.Fatalf("media reoffers = %v (mute must carry over)", m.reoffers)
	}
	if len(activateSpecs(runner, "voice.activate")) != 1 || len(activateSpecs(runner, "voice.deactivate")) != 0 {
		t.Fatal("a reconnect must not touch the provider's voice UI")
	}
	// CONNECTED is announced again once the new peer connection is up.
	if got := r.vtypes(vSession); got[len(got)-1] != vtReconnecting {
		t.Fatalf("events = %v", got)
	}
	m.sink(vSession).PeerConnected()
	if got := r.vtypes(vSession); got[len(got)-1] != vtConnected {
		t.Fatalf("events after the new peer = %v", got)
	}
	if _, err := r.voice.ReconnectVoice(r.ctx(), &helperv1.ReconnectVoiceRequest{Fence: r.vfence(vSession, vAttempt, 1)}); codeOf(err) != "InvalidArgument" {
		t.Fatalf("reconnect without an offer = %v", err)
	}
}

func TestVoiceActivationFailureEndsTheCall(t *testing.T) {
	for _, c := range []struct {
		name     string
		outcome  *helperv1.AttemptOutcome
		wantCode string
		wantProv string
	}{
		{"login wall", &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED, ErrorCode: "helper_worker_blocked"}, "activation_failed", "login_wall"},
		{"worker failure", &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED, ErrorCode: "helper_worker_failed", ErrorMessage: "SECRET page text"}, "activation_failed", "failed"},
		{"not activated", &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, ResultJson: `{"state":"unverified_ready"}`}, "activation_failed", "unverified_ready"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newFakeVoiceMedia()
			runner := newFakeRunner(voiceScript(func() *helperv1.AttemptOutcome { return c.outcome }, nil))
			r := newVoiceRig(t, runner, m)
			r.mustOffer(vSession, vAttempt, 1)
			end := r.waitVoiceEnded(vSession)
			data := dataOf(t, end)
			if end.GetEndReason() != c.wantCode || data["provider_state"] != c.wantProv {
				t.Fatalf("ENDED = %v data=%v", end, data)
			}
			if strings.Contains(end.GetDataJson(), "SECRET") {
				t.Fatalf("worker text leaked: %s", end.GetDataJson())
			}
			if m.closeCount(vSession) == 0 {
				t.Fatal("the media must come down when activation fails")
			}
			// Deactivation is attempted (the UI may be half-activated).
			if len(activateSpecs(runner, "voice.deactivate")) != 1 {
				t.Fatalf("deactivations = %d", len(activateSpecs(runner, "voice.deactivate")))
			}
			if got := r.capacity().GetAttemptsActive(); got != 0 {
				t.Fatalf("capacity leaked: %d", got)
			}
		})
	}
}

func TestVoiceFailedDeactivationIsAWarningAndStillReleases(t *testing.T) {
	m := newFakeVoiceMedia()
	runner := newFakeRunner(voiceScript(nil, func() *helperv1.AttemptOutcome {
		return &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED, ErrorCode: "helper_worker_failed"}
	}))
	r := newVoiceRig(t, runner, m)
	r.mustOffer(vSession, vAttempt, 1)
	eventually(t, "voice.activate", func() bool { return len(activateSpecs(runner, "voice.activate")) == 1 })
	if _, err := r.control(vSession, vAttempt, 1, opTerminate); err != nil {
		t.Fatal(err)
	}
	end := r.waitVoiceEnded(vSession)
	types := r.vtypes(vSession)
	if types[len(types)-2] != vtWarning || dataOf(t, end)["deactivated"] != false {
		t.Fatalf("events = %v, ENDED data = %v", types, dataOf(t, end))
	}
	if got := r.capacity().GetAttemptsActive(); got != 0 {
		t.Fatalf("a failed deactivation must not pin the identity forever: %d", got)
	}
}

func TestVoiceCallEndedWhileActivatingStillDeactivatesAfterTheJob(t *testing.T) {
	m := newFakeVoiceMedia()
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	runner := newFakeRunner(func(ctx context.Context, spec AttemptSpec, emit EmitFunc) error {
		if spec.CommandType == "voice.activate" {
			once.Do(func() { close(started) })
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err() // the call ended: the job is cancelled
			}
			return nil
		}
		return voiceScript(nil, nil)(ctx, spec, emit)
	})
	r := newVoiceRig(t, runner, m)
	r.mustOffer(vSession, vAttempt, 1)
	<-started
	if _, err := r.control(vSession, vAttempt, 1, opTerminate); err != nil {
		t.Fatal(err)
	}
	end := r.waitVoiceEnded(vSession)
	if end.GetEndReason() != "terminated" || len(activateSpecs(runner, "voice.deactivate")) != 1 {
		t.Fatalf("ENDED = %v, deactivations = %d", end, len(activateSpecs(runner, "voice.deactivate")))
	}
	close(release)
}

func TestVoiceMediaEndingOnItsOwnEndsTheCall(t *testing.T) {
	m := newFakeVoiceMedia()
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	r.mustOffer(vSession, vAttempt, 1)
	m.sink(vSession).MediaEnded("peer_connection_failed")
	end := r.waitVoiceEnded(vSession)
	if end.GetEndReason() != "media_ended" || dataOf(t, end)["reason"] != "peer_connection_failed" {
		t.Fatalf("ENDED = %v", end)
	}
	// A hostile reason string is not forwarded.
	m2 := newFakeVoiceMedia()
	r2 := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m2)
	r2.mustOffer(vSession, vAttempt, 1)
	m2.sink(vSession).MediaEnded("relay error: 10.0.0.1 token=abc")
	if got := dataOf(t, r2.waitVoiceEnded(vSession))["reason"]; got != "unknown" {
		t.Fatalf("reason = %v", got)
	}
}

func TestVoiceStreamEventsReplaysAfterASequenceAndEndsAtEnded(t *testing.T) {
	m := newFakeVoiceMedia()
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	r.mustOffer(vSession, vAttempt, 1)
	eventually(t, "provider ready", func() bool {
		c := r.vcall(vSession)
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.providerReady
	})
	m.sink(vSession).PeerConnected()                                    // STARTED(1) CONNECTED(2)
	if _, err := r.control(vSession, vAttempt, 1, opMute); err != nil { // MUTED(3)
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(r.ctx())
	defer cancel()
	stream, err := r.voice.StreamVoiceEvents(ctx, &helperv1.StreamVoiceEventsRequest{Fence: r.vfence(vSession, vAttempt, 1), AfterSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	var seqs []uint64
	for i := 0; i < 2; i++ {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		e := resp.GetEvent()
		if e.GetAttemptId() != vAttempt || e.GetLeaseGeneration() != 1 {
			t.Fatalf("event %v is not stamped with its attempt and generation", e)
		}
		seqs = append(seqs, e.GetSequence())
	}
	if fmt.Sprint(seqs) != "[2 3]" {
		t.Fatalf("replayed sequences = %v, want [2 3]", seqs)
	}
	// The stream follows live events and closes cleanly at ENDED.
	if _, err := r.control(vSession, vAttempt, 1, opTerminate); err != nil {
		t.Fatal(err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetEvent().GetType() != vtEnded || resp.GetEvent().GetEndReason() != "terminated" || resp.GetEvent().GetSequence() != 4 {
		t.Fatalf("last event = %v", resp.GetEvent())
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream after ENDED = %v, want EOF", err)
	}
	// A reader on an ended call still gets the whole tail from the log.
	again, err := r.voice.StreamVoiceEvents(r.ctx(), &helperv1.StreamVoiceEventsRequest{Fence: r.vfence(vSession, vAttempt, 1), AfterSequence: 3})
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := again.Recv(); err != nil || resp.GetEvent().GetType() != vtEnded {
		t.Fatalf("tail = %v, %v", resp, err)
	}
	// A stale fence cannot read the stream either.
	stale, err := r.voice.StreamVoiceEvents(r.ctx(), &helperv1.StreamVoiceEventsRequest{Fence: r.vfence(vSession, vAttempt, 9)})
	if err == nil {
		_, err = stale.Recv()
	}
	if codeOf(err) != "Aborted" {
		t.Fatalf("stale stream = %v, want Aborted", err)
	}
}

func TestVoiceEventLogIsBoundedAndKeepsTheEndedSlot(t *testing.T) {
	m := newFakeVoiceMedia()
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	r.mustOffer(vSession, vAttempt, 1)
	for i := 0; i < 3*maxVoiceEvents; i++ {
		m.sink(vSession).ClientMuted(i%2 == 0)
	}
	if n := len(r.vevents(vSession)); n >= maxVoiceEvents {
		t.Fatalf("log grew to %d", n)
	}
	if _, err := r.control(vSession, vAttempt, 1, opTerminate); err != nil {
		t.Fatal(err)
	}
	r.waitVoiceEnded(vSession)
	if n := len(r.vevents(vSession)); n > maxVoiceEvents {
		t.Fatalf("log = %d events, bound %d", n, maxVoiceEvents)
	}
}

// ---- drain, shutdown, wiring ---------------------------------------------------

func TestVoiceDrainRefusesNewCallsAndCancelsLiveOnesAtGrace(t *testing.T) {
	m := newFakeVoiceMedia()
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	r.mustOffer(vSession, vAttempt, 1)
	if _, err := r.client.Drain(r.ctx(), &helperv1.DrainRequest{NodeId: "helper-1", GraceSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	other := r.offerReq("voice_1700000008_d", "att_voice8", 1)
	other.IdentityRef, other.InstanceRef = "ident-v2", "env-b"
	if _, err := r.offer(other); codeOf(err) != "Unavailable" {
		t.Fatalf("offer while draining = %v, want Unavailable", err)
	}
	if !r.vcall(vSession).live() {
		t.Fatal("drain must let a running call finish")
	}
	r.clock.Advance(61 * time.Second) // the drain grace; the lease (120s) is still valid
	if got := r.waitVoiceEnded(vSession).GetEndReason(); got != "drain_grace_elapsed" {
		t.Fatalf("end_reason = %q", got)
	}
}

func TestVoiceShutdownEndsEveryCallAndWaitsForTeardown(t *testing.T) {
	m := newFakeVoiceMedia()
	runner := newFakeRunner(voiceScript(nil, nil))
	r := newVoiceRig(t, runner, m)
	r.mustOffer(vSession, vAttempt, 1)
	eventually(t, "voice.activate", func() bool { return len(activateSpecs(runner, "voice.activate")) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	end := r.vevents(vSession)
	if last := end[len(end)-1]; last.GetType() != vtEnded || last.GetEndReason() != "shutdown" {
		t.Fatalf("after Shutdown the last event = %v", last)
	}
	if _, err := r.offer(r.offerReq("voice_1700000007_s", "att_voice7", 1)); codeOf(err) != "Unavailable" {
		t.Fatalf("offer after shutdown = %v", err)
	}
}

func TestVoiceIsOffByDefault(t *testing.T) {
	r := newRig(t, newFakeRunner(blockingScript))
	if r.srv.VoiceService() != nil {
		t.Fatal("voice must not be served unless configured")
	}
	// Not registered: the primary's call is answered Unimplemented by the
	// authenticated server (no service of that name exists).
	_, err := r.voice.OfferVoice(r.ctx(), &helperv1.OfferVoiceRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("OfferVoice with voice off = %v, want Unimplemented", err)
	}
	// And the handshake does not advertise it.
	resp, err := r.client.Handshake(r.ctx(), &helperv1.HandshakeRequest{ProtocolVersion: ProtocolVersion, NodeId: "helper-1"})
	if err != nil || strings.Contains(strings.Join(resp.GetFeatures(), ","), "helper_voice") {
		t.Fatalf("features = %v, %v", resp.GetFeatures(), err)
	}
}

func TestVoiceHandshakeAdvertisesTheServiceWhenConfigured(t *testing.T) {
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), newFakeVoiceMedia())
	resp, err := r.client.Handshake(r.ctx(), &helperv1.HandshakeRequest{ProtocolVersion: ProtocolVersion, NodeId: "helper-1"})
	if err != nil || !strings.Contains(strings.Join(resp.GetFeatures(), ","), "helper_voice") {
		t.Fatalf("features = %v, %v", resp.GetFeatures(), err)
	}
}

// A node with no worker cannot activate the provider's voice UI: it refuses the
// call before any media starts instead of answering and then failing.
func TestVoiceWithoutARunnerRefusesBeforeAnyMediaStarts(t *testing.T) {
	m := newFakeVoiceMedia()
	r := newRig(t, nil, voiceCfg(m))
	if _, err := r.offer(r.offerReq(vSession, vAttempt, 1)); codeOf(err) != "Unavailable" {
		t.Fatalf("offer without a runner = %v, want Unavailable", err)
	}
	if m.openCount() != 0 {
		t.Fatal("media was opened for a call that can never be activated")
	}
	if got := r.capacity().GetAttemptsActive(); got != 0 {
		t.Fatalf("capacity leaked: %d", got)
	}
}

// A late Close for an older generation never reaches the successor's media: the
// service addresses media by attempt and generation, not by session.
func TestVoiceSupersedingGenerationClosesOnlyTheOlderCallsMedia(t *testing.T) {
	m := newFakeVoiceMedia()
	r := newVoiceRig(t, newFakeRunner(voiceScript(nil, nil)), m)
	r.mustOffer(vSession, vAttempt, 3)
	next := r.offerReq(vSession, "att_voice9", 4)
	eventually(t, "the newer generation to be admitted", func() bool {
		_, err := r.offer(next)
		return err == nil
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closes[VoiceCallKey(vAttempt, 3)] == 0 {
		t.Fatal("the superseded call's media was not closed")
	}
	if m.closes[VoiceCallKey("att_voice9", 4)] != 0 {
		t.Fatal("the successor's media was closed along with the older call's")
	}
}

func TestVoiceConfigIsValidated(t *testing.T) {
	good := VoiceEnvironment{ID: "env-a", CDPEndpoint: "http://127.0.0.1:9222", RelayAddr: "127.0.0.1:9099", RelayKeyFile: "/k"}
	for name, v := range map[string]*VoiceConfig{
		"no media":         {Environments: []VoiceEnvironment{good}},
		"no environments":  {Media: newFakeVoiceMedia()},
		"duplicate id":     {Media: newFakeVoiceMedia(), Environments: []VoiceEnvironment{good, good}},
		"no relay address": {Media: newFakeVoiceMedia(), Environments: []VoiceEnvironment{{ID: "e", CDPEndpoint: "x", RelayKeyFile: "/k"}}},
		"no key file":      {Media: newFakeVoiceMedia(), Environments: []VoiceEnvironment{{ID: "e", CDPEndpoint: "x", RelayAddr: "y"}}},
		"bad id":           {Media: newFakeVoiceMedia(), Environments: []VoiceEnvironment{{ID: "../e", CDPEndpoint: "x", RelayAddr: "y", RelayKeyFile: "/k"}}},
	} {
		_, err := NewServer(Config{NodeID: "helper-1", WorkloadVersion: "w1", RegistryDigest: testDigest, Voice: v})
		if err == nil {
			t.Errorf("%s: NewServer accepted an invalid voice config", name)
		}
	}
	if _, err := NewServer(Config{NodeID: "helper-1", WorkloadVersion: "w1", RegistryDigest: testDigest, Voice: &VoiceConfig{Media: newFakeVoiceMedia(), Environments: []VoiceEnvironment{good}}}); err != nil {
		t.Fatal(err)
	}
}
