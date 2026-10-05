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

func activationServer(t *testing.T, state string, gate chan struct{}) (*Server, http.Handler, *voiceSimExecutor) {
	t.Helper()
	store := jobstore.NewMemoryStore()
	sim := &voiceSimExecutor{recordingExecutor: &recordingExecutor{}, store: store, gate: gate, state: state}
	srv, h, _ := voiceTestServer(t, func(c *Config) {
		c.Jobs, c.Executor, c.VoiceProviderActivation = store, sim, true
		c.Topology = topology.NewMemoryStore()
		addVoiceEnvironment(c, "1")
		c.Topology.(*topology.MemoryStore).AddInstance(topology.BrowserInstance{
			InstanceID: "browser-1", TenantID: "tenant_edge", State: "ready", RemoteEndpoint: "http://172.28.0.10:9223"})
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
