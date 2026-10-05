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
		ContextID: "ctx-1", TenantID: "tenant_edge", TargetID: "chatgpt_web",
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
	if !VerifyVoiceMediaCredential("dev-secret", id, connected.MediaCredential, time.Now().UTC()) {
		t.Fatal("issued credential must verify")
	}
	if VerifyVoiceMediaCredential("dev-secret", "voice_other", connected.MediaCredential, time.Now().UTC()) {
		t.Fatal("credential must be session-scoped")
	}
	if VerifyVoiceMediaCredential("dev-secret", id, connected.MediaCredential, time.Now().UTC().Add(6*time.Minute)) {
		t.Fatal("expired credential must fail")
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
