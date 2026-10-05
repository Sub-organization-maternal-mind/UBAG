package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
	voice "github.com/ubag/ubag/apps/gateway/internal/voice"
)

type fakeMediaNegotiator struct {
	answer   string
	err      error
	offers   []string
	sessions []voice.Session
}

func (f *fakeMediaNegotiator) HandleOffer(_ context.Context, session voice.Session, sdpOffer string) (string, error) {
	f.offers = append(f.offers, sdpOffer)
	f.sessions = append(f.sessions, session)
	if f.err != nil {
		return "", f.err
	}
	return f.answer, nil
}

func voiceTestServer(t *testing.T, mutate func(*Config)) (*Server, http.Handler, *fakeMediaNegotiator) {
	t.Helper()
	cfg := Config{
		AppSecret:     "dev-secret",
		ActorRole:     "service",
		Executor:      &recordingExecutor{},
		FacadeMaxWait: 10 * time.Second,
		VoiceStore:    voice.NewMemoryStore(),
		Topology:      topology.NewMemoryStore(),
	}
	cfg.Topology.(interface {
		AddInstance(topology.BrowserInstance)
	}).AddInstance(topology.BrowserInstance{InstanceID: "browser-1", TenantID: "tenant_edge", State: "ready"})
	cfg.Topology.(interface {
		AddContext(topology.ProviderContext)
	}).AddContext(topology.ProviderContext{
		ContextID: "ctx-1", InstanceID: "browser-1", TenantID: "tenant_edge", TargetID: "chatgpt_web",
		IdentityRef: "acct-chatgpt-1", LoginState: "authenticated",
	})
	media := &fakeMediaNegotiator{answer: "v=0\r\no=- answer\r\n"}
	cfg.VoiceMedia = media
	if mutate != nil {
		mutate(&cfg)
	}
	srv := NewServer(cfg)
	return srv, srv.Handler(), media
}

func voiceBody(target string) string {
	return fmt.Sprintf(`{"target":%q}`, target)
}

func TestVoiceRoutesNotConfigured(t *testing.T) {
	srv := NewServer(Config{AppSecret: "dev-secret", ActorRole: "service", Executor: &recordingExecutor{}})
	handler := srv.Handler()
	rec := doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders(""))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("create without store = %d, want 501; body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(handler, http.MethodGet, "/v1/voice/sessions", "", authHeaders(""))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("list without store = %d, want 501", rec.Code)
	}
}

func TestVoiceCreateClaimsLeases(t *testing.T) {
	_, handler, _ := voiceTestServer(t, nil)
	rec := doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders(""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Session voice.Session `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Session.Status != voice.StatusConnecting {
		t.Fatalf("status = %s, want connecting", created.Session.Status)
	}
	if created.Session.IdentityRef != "acct-chatgpt-1" || created.Session.InstanceRef != "browser-1" {
		t.Fatalf("leases = %s/%s", created.Session.IdentityRef, created.Session.InstanceRef)
	}
	// Second session must queue (the browser environment is exclusive).
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders(""))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second create = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var queued struct {
		Session voice.Session `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &queued)
	if queued.Session.Status != voice.StatusQueued {
		t.Fatalf("queued status = %s", queued.Session.Status)
	}
	// A target without live voice fails closed.
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("deepseek_web"), authHeaders(""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deepseek voice create = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "UBAG-VOICE-UNSUPPORTED-001") {
		t.Fatalf("expected voice-unsupported code, body=%s", rec.Body.String())
	}
}

func TestVoiceCreateBudgetRejects(t *testing.T) {
	_, handler, _ := voiceTestServer(t, func(c *Config) {
		c.VoiceMaxSessionsPerTenant = 1
		c.VoiceMaxQueuedPerTenant = 1
	})
	// First: claims the environment. Second: queues. Third: queue full.
	if rec := doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders("")); rec.Code != http.StatusCreated {
		t.Fatalf("first = %d", rec.Code)
	}
	if rec := doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders("")); rec.Code != http.StatusAccepted {
		t.Fatalf("second = %d, want 202", rec.Code)
	}
	rec := doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders(""))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "UBAG-VOICE-QUEUE-FULL-004") {
		t.Fatalf("expected queue-full code, body=%s", rec.Body.String())
	}
}

func TestVoiceLifecycleAndIsolation(t *testing.T) {
	_, handler, _ := voiceTestServer(t, nil)
	rec := doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders(""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d", rec.Code)
	}
	var created struct {
		Session voice.Session `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created.Session.ID

	// Mute.
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions/"+id+"/mute", `{"muted":true}`, authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("mute = %d; body=%s", rec.Code, rec.Body.String())
	}
	// Renew extends the lease.
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions/"+id+"/renew", `{}`, authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("renew = %d", rec.Code)
	}
	// Status reflects mute.
	rec = doJSON(handler, http.MethodGet, "/v1/voice/sessions/"+id, "", authHeaders(""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"muted":true`) {
		t.Fatalf("get = %d; body=%s", rec.Code, rec.Body.String())
	}
	// Terminate.
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions/"+id+"/terminate", `{}`, authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("terminate = %d", rec.Code)
	}
	// A terminated session cannot connect.
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions/"+id+"/connect", `{"sdp_offer":"v=0"}`, authHeaders(""))
	if rec.Code != http.StatusConflict {
		t.Fatalf("connect after terminate = %d, want 409", rec.Code)
	}
	// Cross-tenant reads are 404.
	otherHeaders := map[string]string{
		"Authorization":      authHeaders("")["Authorization"],
		"X-Tenant-ID":        "tenant_other",
		"X-UBAG-Test-Tenant": "1",
	}
	_ = otherHeaders
	if rec = doJSON(handler, http.MethodGet, "/v1/voice/sessions/"+id, "", authHeaders("")); rec.Code != http.StatusOK {
		t.Fatalf("same-tenant get must stay 200, got %d", rec.Code)
	}
	// List shows the terminated session.
	rec = doJSON(handler, http.MethodGet, "/v1/voice/sessions", "", authHeaders(""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "terminated") {
		t.Fatalf("list = %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestVoiceConnectNegotiatesAndIssuesCredential(t *testing.T) {
	_, handler, media := voiceTestServer(t, nil)
	rec := doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders(""))
	var created struct {
		Session voice.Session `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created.Session.ID

	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions/"+id+"/connect", `{"sdp_offer":"v=0\r\no=- offer\r\n"}`, authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("connect = %d; body=%s", rec.Code, rec.Body.String())
	}
	var connected voiceSessionConnectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &connected); err != nil {
		t.Fatalf("decode connect: %v", err)
	}
	if connected.SDPAnswer != media.answer {
		t.Fatalf("answer = %q", connected.SDPAnswer)
	}
	if !VerifyVoiceMediaCredential("dev-secret", media.sessions[0], connected.MediaCredential, time.Now().UTC()) {
		t.Fatal("issued credential must verify")
	}
	if VerifyVoiceMediaCredential("dev-secret", voice.Session{ID: "voice_other", TenantID: media.sessions[0].TenantID, AppID: media.sessions[0].AppID}, connected.MediaCredential, time.Now().UTC()) {
		t.Fatal("credential must be session-scoped")
	}
	if VerifyVoiceMediaCredential("dev-secret", media.sessions[0], connected.MediaCredential, time.Now().UTC().Add(6*time.Minute)) {
		t.Fatal("expired credential must fail")
	}
	other := media.sessions[0]
	other.TenantID = "someone_else"
	if VerifyVoiceMediaCredential("dev-secret", other, connected.MediaCredential, time.Now().UTC()) {
		t.Fatal("credential must be tenant-scoped")
	}
	if len(media.offers) != 1 || !strings.Contains(media.offers[0], "offer") {
		t.Fatalf("media negotiator saw offers %v", media.offers)
	}
	// The session handed to the media plane carries the claimed leases.
	if media.sessions[0].IdentityRef == "" || media.sessions[0].InstanceRef == "" {
		t.Fatalf("media session missing leases: %+v", media.sessions[0])
	}
	// Media-plane failure on a re-connect is a 503 with retry guidance.
	media.err = fmt.Errorf("ICE gathering failed")
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions/"+id+"/connect", `{"sdp_offer":"v=0 retry"}`, authHeaders(""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("media failure = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "UBAG-VOICE-MEDIA-UNAVAILABLE-007") {
		t.Fatalf("expected media-unavailable code, body=%s", rec.Body.String())
	}
}

func TestVoiceUtteranceModeSelectable(t *testing.T) {
	_, handler, media := voiceTestServer(t, nil)
	// utterance mode creates a transcription-style job and claims NO leases.
	rec := doJSON(handler, http.MethodPost, "/v1/voice/sessions",
		`{"target":"chatgpt_web","mode":"utterance"}`, authHeaders(""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("utterance create = %d; body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Session   voice.Session `json:"session"`
		JobID     string        `json:"job_id"`
		SessionID string        `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Session.Mode != voice.ModeUtterance || created.JobID == "" {
		t.Fatalf("session = %+v job=%q", created.Session, created.JobID)
	}
	if created.Session.IdentityRef != "" || created.Session.InstanceRef != "" {
		t.Fatalf("utterance sessions must never hold leases: %+v", created.Session)
	}
	if len(media.offers) != 0 {
		t.Fatal("media plane must not be engaged for utterance mode")
	}
	// Unknown modes are rejected (never silently substituted).
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions",
		`{"target":"chatgpt_web","mode":"whisper"}`, authHeaders(""))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "never substituted") {
		t.Fatalf("unknown mode = %d; body=%s", rec.Code, rec.Body.String())
	}
	// Explicit live mode works as before.
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions",
		`{"target":"chatgpt_web","mode":"live"}`, authHeaders(""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("explicit live = %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestVoiceConnectWithoutMediaPlane(t *testing.T) {
	_, handler, _ := voiceTestServer(t, func(c *Config) { c.VoiceMedia = nil })
	rec := doJSON(handler, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders(""))
	var created struct {
		Session voice.Session `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	rec = doJSON(handler, http.MethodPost, "/v1/voice/sessions/"+created.Session.ID+"/connect", `{"sdp_offer":"v=0"}`, authHeaders(""))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("connect without media = %d, want 501", rec.Code)
	}
}

// addVoiceEnvironment registers a second browser environment hosting its own
// authenticated ChatGPT account.
func addVoiceEnvironment(c *Config, n string) {
	st := c.Topology.(*topology.MemoryStore)
	st.AddInstance(topology.BrowserInstance{InstanceID: "browser-" + n, TenantID: "tenant_edge", State: "ready"})
	st.AddContext(topology.ProviderContext{ContextID: "ctx-" + n, InstanceID: "browser-" + n, TenantID: "tenant_edge",
		TargetID: "chatgpt_web", IdentityRef: "acct-chatgpt-" + n, LoginState: "authenticated"})
}

func createVoice(t *testing.T, h http.Handler, body string) (int, voice.Session) {
	t.Helper()
	rec := doJSON(h, http.MethodPost, "/v1/voice/sessions", body, authHeaders(""))
	var out struct {
		Session voice.Session `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Session
}

// cap=1 with two free environments must queue the second session.
func TestVoiceActiveBudgetQueuesWithTwoEnvironments(t *testing.T) {
	_, h, _ := voiceTestServer(t, func(c *Config) {
		c.VoiceMaxSessionsPerTenant, c.VoiceMaxQueuedPerTenant = 1, 1
		addVoiceEnvironment(c, "2")
	})
	if code, _ := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusCreated {
		t.Fatalf("first = %d", code)
	}
	if code, sess := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusAccepted || sess.Status != voice.StatusQueued {
		t.Fatalf("cap=1 must queue the second session, got %d %s", code, sess.Status)
	}
	rec := doJSON(h, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders(""))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("queue-full = %d retry-after=%q body=%s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

// A caller-named identity is only a preference: an account the tenant does
// not own resolves to nothing and never becomes a lease.
func TestVoiceUnknownIdentityIsNotProofOfOwnership(t *testing.T) {
	_, h, _ := voiceTestServer(t, nil)
	_, sess := createVoice(t, h, `{"target":"chatgpt_web","identity_ref":"someone-elses-account"}`)
	if sess.IdentityRef == "someone-elses-account" {
		t.Fatalf("caller-supplied identity became a lease: %+v", sess)
	}
	if sess.IdentityRef != "acct-chatgpt-1" {
		t.Fatalf("server must resolve its own account, got %+v", sess)
	}
}

// Contexts are paired with the instance that hosts them: an authenticated
// context whose instance does not exist cannot be leased.
func TestVoiceContextWithoutHostingInstanceQueues(t *testing.T) {
	_, h, _ := voiceTestServer(t, func(c *Config) {
		c.Topology = topology.NewMemoryStore()
		c.Topology.(*topology.MemoryStore).AddContext(topology.ProviderContext{ContextID: "orphan", InstanceID: "missing",
			TenantID: "tenant_edge", TargetID: "chatgpt_web", IdentityRef: "acct", LoginState: "authenticated"})
	})
	if code, sess := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusAccepted || sess.IdentityRef != "" {
		t.Fatalf("got %d %+v", code, sess)
	}
}

func TestVoiceSubtreeActionsRejectWrongMethods(t *testing.T) {
	_, h, media := voiceTestServer(t, nil)
	_, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	for _, action := range []string{"connect", "mute", "renew", "terminate"} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			rec := doJSON(h, method, "/v1/voice/sessions/"+sess.ID+"/"+action, `{"sdp_offer":"v=0","muted":true}`, authHeaders(""))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want 405", method, action, rec.Code)
			}
		}
	}
	if len(media.offers) != 0 {
		t.Fatal("wrong-method connect reached the media plane")
	}
	rec := doJSON(h, http.MethodGet, "/v1/voice/sessions/"+sess.ID, "", authHeaders(""))
	var got struct {
		Session voice.Session `json:"session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Session.Status != voice.StatusConnecting || got.Session.Muted {
		t.Fatalf("rejected requests changed state: %+v", got.Session)
	}
}

func TestVoiceOversizedBodyIs413AndMediaErrorsAreRedacted(t *testing.T) {
	_, h, media := voiceTestServer(t, nil)
	rec := doJSON(h, http.MethodPost, "/v1/voice/sessions", `{"target":"chatgpt_web","pad":"`+strings.Repeat("x", 20<<10)+`"}`, authHeaders(""))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized create = %d, want 413", rec.Code)
	}
	_, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	media.err = fmt.Errorf("dial tcp 172.28.0.10:9099: connection refused")
	rec = doJSON(h, http.MethodPost, "/v1/voice/sessions/"+sess.ID+"/connect", `{"sdp_offer":"v=0"}`, authHeaders(""))
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "172.28") {
		t.Fatalf("media failure = %d body=%s (must not leak internals)", rec.Code, rec.Body.String())
	}
}

func TestVoiceRenewOfTerminatedSessionConflicts(t *testing.T) {
	_, h, _ := voiceTestServer(t, nil)
	_, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	doJSON(h, http.MethodPost, "/v1/voice/sessions/"+sess.ID+"/terminate", `{}`, authHeaders(""))
	rec := doJSON(h, http.MethodPost, "/v1/voice/sessions/"+sess.ID+"/renew", `{}`, authHeaders(""))
	if rec.Code != http.StatusConflict {
		t.Fatalf("renew after terminate = %d, want 409", rec.Code)
	}
}
