package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	voice "github.com/ubag/ubag/apps/gateway/internal/voice"
)

// voiceSimExecutor stands in for the worker: it records dispatched jobs and
// completes voice control jobs through the real job store, optionally held
// back by gate so tests can order readiness signals.
type voiceSimExecutor struct {
	*recordingExecutor
	store jobstore.Store
	gate  chan struct{} // activation waits for this to close; nil = immediate
	state string        // the worker's activation state
	// deactivateGate holds the deactivation job until closed (nil = immediate),
	// so a test can observe the terminating hold while teardown is in flight.
	deactivateGate chan struct{}

	mu   sync.Mutex
	sent []jobstore.Job
}

func (e *voiceSimExecutor) EnqueueJob(ctx context.Context, job jobstore.Job) (executor.Receipt, error) {
	e.mu.Lock()
	e.sent = append(e.sent, job)
	e.mu.Unlock()
	go func() {
		state := e.state
		if job.CommandType == "voice.activate" && e.gate != nil {
			<-e.gate
		}
		if job.CommandType == "voice.deactivate" {
			if e.deactivateGate != nil {
				<-e.deactivateGate
			}
			state = "deactivated"
		}
		evType := "completed"
		data := map[string]any{"status": "completed", "result": map[string]any{"state": state}}
		if state != "activated" && state != "deactivated" {
			evType = "failed"
			data = map[string]any{"status": "failed", "retryable": false, "result": map[string]any{"state": state}}
		}
		_, _, _ = e.store.ApplyWorkerEvent(context.Background(), jobstore.WorkerEvent{
			EventID: "evt_" + job.ID, JobID: job.ID, APIVersion: job.APIVersion,
			Type: evType, TraceID: job.TraceID, Data: data,
		})
	}()
	return executor.Receipt{Backend: "sim", QueueName: "jobs", MessageID: job.ID, EnqueuedAt: time.Now().UTC()}, nil
}

func (e *voiceSimExecutor) jobs() []jobstore.Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]jobstore.Job(nil), e.sent...)
}

func activationServer(t *testing.T, state string, gate chan struct{}, mutate ...func(*Config)) (*Server, http.Handler, *voiceSimExecutor) {
	t.Helper()
	store := jobstore.NewMemoryStore()
	sim := &voiceSimExecutor{recordingExecutor: &recordingExecutor{}, store: store, gate: gate, state: state}
	srv, h, _ := voiceTestServer(t, func(c *Config) {
		c.Jobs, c.Executor, c.VoiceProviderActivation = store, sim, true
		c.Topology = topology.NewMemoryStore()
		addVoiceEnvironment(c, "1")
		c.Topology.(*topology.MemoryStore).AddInstance(topology.BrowserInstance{
			InstanceID: "browser-1", TenantID: "tenant_edge", State: "ready", RemoteEndpoint: "http://172.28.0.10:9223"})
		for _, fn := range mutate {
			fn(c)
		}
	})
	return srv, h, sim
}

func getVoice(t *testing.T, h http.Handler, id string) voice.Session {
	t.Helper()
	rec := doJSON(h, http.MethodGet, "/v1/voice/sessions/"+id, "", authHeaders(""))
	var out struct {
		Session voice.Session `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Session
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func connectVoice(t *testing.T, h http.Handler, id string) {
	t.Helper()
	rec := doJSON(h, http.MethodPost, "/v1/voice/sessions/"+id+"/connect", `{"sdp_offer":"v=0"}`, authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("connect = %d body=%s", rec.Code, rec.Body.String())
	}
}

// A session is connected only when the provider is verified ready AND the
// client's media is up — in either order, never from either alone.
func TestVoiceConnectedRequiresProviderReadyAndPeerUp(t *testing.T) {
	gate := make(chan struct{})
	srv, h, sim := activationServer(t, "activated", gate)
	_, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	connectVoice(t, h, sess.ID)

	eventually(t, "activation job dispatch", func() bool { return len(sim.jobs()) == 1 })
	job := sim.jobs()[0]
	if job.CommandType != "voice.activate" || job.AppID != voiceControlAppID || job.TenantID != "tenant_edge" ||
		job.Target != "chatgpt_web" || job.Input["cdp_endpoint"] != "http://172.28.0.10:9223" || job.TraceID == "" {
		t.Fatalf("control job = %+v", job)
	}

	srv.VoiceMediaConnected(sess) // peer up, provider NOT yet ready
	time.Sleep(100 * time.Millisecond)
	if got := getVoice(t, h, sess.ID).Status; got != voice.StatusConnecting {
		t.Fatalf("status = %s before the provider is ready, want connecting", got)
	}
	close(gate) // worker reports verified-ready
	eventually(t, "connected", func() bool { return getVoice(t, h, sess.ID).Status == voice.StatusConnected })
}

func TestVoiceActivationFailureTerminatesWithExplicitState(t *testing.T) {
	srv, h, _ := activationServer(t, "login_wall", nil)
	_, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	connectVoice(t, h, sess.ID)
	srv.VoiceMediaConnected(sess)
	eventually(t, "termination", func() bool { return getVoice(t, h, sess.ID).Status == voice.StatusTerminated })
	if got := getVoice(t, h, sess.ID).LastError; !strings.Contains(got, "activation_failed") || !strings.Contains(got, "login_wall") {
		t.Fatalf("last_error = %q, want the worker's explicit state", got)
	}
}

func TestVoiceMediaEndDeactivatesProvider(t *testing.T) {
	srv, h, sim := activationServer(t, "activated", nil)
	_, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	connectVoice(t, h, sess.ID)
	eventually(t, "activation", func() bool { return len(sim.jobs()) == 1 })
	srv.VoiceMediaEnded(sess, "session_terminated")
	eventually(t, "deactivation job", func() bool {
		jobs := sim.jobs()
		return len(jobs) == 2 && jobs[1].CommandType == "voice.deactivate"
	})
}

func terminateVoice(t *testing.T, h http.Handler, id string) {
	t.Helper()
	if rec := doJSON(h, http.MethodPost, "/v1/voice/sessions/"+id+"/terminate", `{}`, authHeaders("")); rec.Code != http.StatusOK {
		t.Fatalf("terminate = %d body=%s", rec.Code, rec.Body.String())
	}
}

// With the terminating hold on, an explicit terminate reads as terminated at
// once but the account and environment stay reserved until the provider's
// deactivation is acked; only then does a successor get them. (P5.8, the
// lease-released-before-deactivate race.)
func TestVoiceTerminatingHoldKeepsLeasesUntilDeactivateAck(t *testing.T) {
	store := voice.NewMemoryStore()
	release := make(chan struct{})
	srv, h, sim := activationServer(t, "activated", nil, func(c *Config) {
		c.VoiceStore, c.VoiceTerminatingHold = store, true
	})
	sim.deactivateGate = release
	_, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	connectVoice(t, h, sess.ID)
	eventually(t, "activation job", func() bool { return len(sim.jobs()) == 1 })

	terminateVoice(t, h, sess.ID)
	if got := getVoice(t, h, sess.ID); got.Status != voice.StatusTerminated || got.IdentityRef != "" || got.InstanceRef != "" {
		t.Fatalf("terminated session = %+v, want terminated with no leases reported", got)
	}
	held, _, _ := store.Get(t.Context(), "tenant_edge", sess.ID)
	if held.TerminatingUntil.IsZero() {
		t.Fatal("terminate did not start the hold")
	}
	if code, next := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusAccepted || next.Status != voice.StatusQueued {
		t.Fatalf("a successor took the leases during teardown: %d %s", code, next.Status)
	}

	srv.VoiceMediaEnded(sess, "session_terminated") // the media hub's end-of-media hook
	eventually(t, "deactivation job", func() bool { return len(sim.jobs()) == 2 })
	if code, next := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusAccepted || next.Status != voice.StatusQueued {
		t.Fatalf("a successor took the leases before the deactivate ack: %d %s", code, next.Status)
	}

	close(release) // the worker acks deactivation
	eventually(t, "hold release", func() bool {
		got, _, _ := store.Get(t.Context(), "tenant_edge", sess.ID)
		return got.TerminatingUntil.IsZero()
	})
	if code, next := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusCreated || next.Status != voice.StatusConnecting {
		t.Fatalf("successor after the ack = %d %s, want connecting", code, next.Status)
	}
}

// A node-bound, terminating session still serializes without any internal
// lease state: the node id never reaches a client.
func TestVoiceSessionResponsesHideInternalLeaseState(t *testing.T) {
	store := voice.NewMemoryStore()
	_, h, _ := voiceTestServer(t, func(c *Config) { c.VoiceStore = store })
	_, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	if _, err := store.BindNode(t.Context(), "tenant_edge", sess.ID, "node-secret-7", time.Minute, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/voice/sessions/" + sess.ID, "/v1/voice/sessions"} {
		body := doJSON(h, http.MethodGet, path, "", authHeaders("")).Body.String()
		for _, leak := range []string{"node-secret-7", "node_id", "lease_generation", "media_lease", "terminating"} {
			if strings.Contains(body, leak) {
				t.Errorf("GET %s leaks %q: %s", path, leak, body)
			}
		}
	}
}

// Without the flag, or when provider voice was never requested for the
// session, terminate frees the leases at once exactly as before.
func TestVoiceTerminateWithoutHoldReleasesImmediately(t *testing.T) {
	t.Run("flag off", func(t *testing.T) {
		_, h, _ := activationServer(t, "activated", nil)
		_, sess := createVoice(t, h, voiceBody("chatgpt_web"))
		connectVoice(t, h, sess.ID)
		terminateVoice(t, h, sess.ID)
		if code, next := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusCreated || next.Status != voice.StatusConnecting {
			t.Fatalf("successor = %d %s, want connecting", code, next.Status)
		}
	})
	t.Run("flag on but voice never activated", func(t *testing.T) {
		_, h, _ := activationServer(t, "activated", nil, func(c *Config) { c.VoiceTerminatingHold = true })
		_, sess := createVoice(t, h, voiceBody("chatgpt_web")) // never connected: nothing to deactivate
		terminateVoice(t, h, sess.ID)
		if code, next := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusCreated || next.Status != voice.StatusConnecting {
			t.Fatalf("successor = %d %s, want connecting", code, next.Status)
		}
	})
}

func TestVoiceControlCommandsAreReservedFromExternalCallers(t *testing.T) {
	_, h, _ := voiceTestServer(t, nil)
	body := `{"api_version":"2026-05-22","idempotency_key":"reserved_voice_key_1","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"voice.activate","input":{"provider_id":"chatgpt_web","cdp_endpoint":"http://evil.example:9222"}}}`
	rec := doJSON(h, http.MethodPost, "/v1/jobs", body, authHeaders("reserved_voice_key_1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("external voice.activate = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestVoiceOriginPolicy(t *testing.T) {
	_, h, _ := voiceTestServer(t, func(c *Config) { c.AllowedOrigins = []string{"https://app.example.com"} })
	call := func(method, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v1/voice/sessions", nil)
		for k, v := range authHeaders("") {
			req.Header.Set(k, v)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(http.MethodGet, ""); rec.Code != http.StatusOK {
		t.Fatalf("server-to-server (no Origin) = %d", rec.Code)
	}
	if rec := call(http.MethodGet, "https://app.example.com"); rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("allowed origin = %d acao=%q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec := call(http.MethodGet, "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin = %d, want 403", rec.Code)
	}
	if rec := call(http.MethodOptions, "https://app.example.com"); rec.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204", rec.Code)
	}
	if got := parseAllowedOrigins("https://a.example/, *, https://b.example"); len(got) != 2 {
		t.Fatalf("wildcard must never be honoured: %v", got)
	}
}

// multiContextServer hosts several provider contexts on one environment so
// control jobs must say which browser context to drive. Creation order (not
// context id order) defines the index.
func multiContextServer(t *testing.T, indexFlag bool, contexts ...topology.ProviderContext) (*Server, *voiceSimExecutor) {
	t.Helper()
	store := jobstore.NewMemoryStore()
	sim := &voiceSimExecutor{recordingExecutor: &recordingExecutor{}, store: store, state: "activated"}
	srv, _, _ := voiceTestServer(t, func(c *Config) {
		c.Jobs, c.Executor, c.VoiceProviderActivation, c.VoiceContextIndex = store, sim, true, indexFlag
		topo := topology.NewMemoryStore()
		topo.AddInstance(topology.BrowserInstance{
			InstanceID: "browser-1", TenantID: "tenant_edge", State: "ready", RemoteEndpoint: "http://172.28.0.10:9223"})
		for _, pc := range contexts {
			topo.AddContext(pc)
		}
		c.Topology = topo
	})
	return srv, sim
}

func ctxRow(id, identity string, created time.Time) topology.ProviderContext {
	return topology.ProviderContext{ContextID: id, InstanceID: "browser-1", TenantID: "tenant_edge",
		TargetID: "chatgpt_web", IdentityRef: identity, LoginState: "authenticated", CreatedAt: created}
}

func TestVoiceControlJobCarriesContextIndexForMultiContextPlacement(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	srv, sim := multiContextServer(t, true,
		ctxRow("ctx-a", "acct-a", t0.Add(time.Second)), // sorts first by id, opened second
		ctxRow("ctx-z", "acct-z", t0))
	for identity, want := range map[string]int{"acct-z": 0, "acct-a": 1} {
		sess := voice.Session{ID: "vs-" + identity, TenantID: "tenant_edge", Target: "chatgpt_web",
			IdentityRef: identity, InstanceRef: "browser-1"}
		if _, err := srv.runVoiceControl(context.Background(), sess, "activate", 5*time.Second); err != nil {
			t.Fatalf("%s: %v", identity, err)
		}
		jobs := sim.jobs()
		if got := jobs[len(jobs)-1].Input["context"]; got != want {
			t.Fatalf("%s: context = %v, want %d", identity, got, want)
		}
	}
}

func TestVoiceControlJobContextIndexIsOptIn(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	both := []topology.ProviderContext{ctxRow("ctx-a", "acct-a", t0), ctxRow("ctx-z", "acct-z", t0.Add(time.Second))}
	sess := voice.Session{ID: "vs-1", TenantID: "tenant_edge", Target: "chatgpt_web", IdentityRef: "acct-z", InstanceRef: "browser-1"}

	srv, sim := multiContextServer(t, false, both...) // flag off: today's job shape
	if _, err := srv.runVoiceControl(context.Background(), sess, "activate", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, has := sim.jobs()[0].Input["context"]; has {
		t.Fatalf("flag off must not add context: %+v", sim.jobs()[0].Input)
	}

	srv, sim = multiContextServer(t, true, both[1]) // single context: nothing to disambiguate
	if _, err := srv.runVoiceControl(context.Background(), sess, "activate", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, has := sim.jobs()[0].Input["context"]; has {
		t.Fatalf("single context must not add context: %+v", sim.jobs()[0].Input)
	}
}

func TestVoiceControlFailsClosedWhenContextCannotBePinned(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	srv, sim := multiContextServer(t, true, ctxRow("ctx-a", "acct-a", t0), ctxRow("ctx-z", "acct-z", t0.Add(time.Second)))
	sess := voice.Session{ID: "vs-1", TenantID: "tenant_edge", Target: "chatgpt_web", IdentityRef: "acct-unknown", InstanceRef: "browser-1"}
	if _, err := srv.runVoiceControl(context.Background(), sess, "activate", time.Second); err == nil {
		t.Fatal("unregistered identity on a multi-context environment must fail closed")
	}
	if n := len(sim.jobs()); n != 0 {
		t.Fatalf("no control job may be dispatched, got %d", n)
	}
}
