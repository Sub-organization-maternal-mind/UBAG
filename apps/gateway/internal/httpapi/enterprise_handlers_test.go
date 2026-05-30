package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/ratelimit"
	"github.com/ubag/ubag/apps/gateway/internal/responsecache"
	"github.com/ubag/ubag/apps/gateway/internal/scim"
	"github.com/ubag/ubag/apps/gateway/internal/session"
	"github.com/ubag/ubag/apps/gateway/internal/siem"
	"github.com/ubag/ubag/apps/gateway/internal/sso"
	"github.com/ubag/ubag/apps/gateway/internal/workflow"
)

func scimHeaders(idempotencyKey string) map[string]string {
	headers := authHeaders(idempotencyKey)
	headers["Content-Type"] = scim.ContentType
	return headers
}

func TestCacheEnabledStatusAndPurge(t *testing.T) {
	cache := responsecache.New(responsecache.NewMemoryStore(), responsecache.Options{TTL: time.Minute, Enabled: true})
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", ResponseCache: cache}).Handler()

	status := doJSON(server, http.MethodGet, "/v1/cache", "", authHeaders(""))
	if status.Code != http.StatusOK {
		t.Fatalf("cache status = %d; body=%s", status.Code, status.Body.String())
	}
	var enabled cacheStatusEnabledResponse
	if err := json.Unmarshal(status.Body.Bytes(), &enabled); err != nil {
		t.Fatalf("decode cache status: %v", err)
	}
	if !enabled.Enabled || enabled.Profile != "edge" {
		t.Fatalf("unexpected cache status: %+v", enabled)
	}

	purge := doJSON(server, http.MethodDelete, "/v1/cache", "", authHeaders(""))
	if purge.Code != http.StatusOK {
		t.Fatalf("cache purge = %d; body=%s", purge.Code, purge.Body.String())
	}
}

func TestRateLimitMiddlewareEnforced(t *testing.T) {
	resolver := ratelimit.NewPolicyResolver(ratelimit.Policy{Limit: 1, Window: time.Minute}, nil)
	limiter := ratelimit.New(ratelimit.NewMemoryStore(), resolver.Default())
	server := NewServer(Config{
		AppSecret:         "dev-secret",
		RateLimiter:       limiter,
		RateLimitResolver: resolver,
		RateLimitEnabled:  true,
	}).Handler()

	first := doJSON(server, http.MethodGet, "/v1/jobs", "", authHeaders(""))
	if first.Code != http.StatusOK {
		t.Fatalf("first request = %d; body=%s", first.Code, first.Body.String())
	}
	second := doJSON(server, http.MethodGet, "/v1/jobs", "", authHeaders(""))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d; want 429; body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header on 429")
	}
}

func TestRateLimitPassThroughWhenDisabled(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()
	for i := 0; i < 3; i++ {
		resp := doJSON(server, http.MethodGet, "/v1/jobs", "", authHeaders(""))
		if resp.Code != http.StatusOK {
			t.Fatalf("request %d = %d; body=%s", i, resp.Code, resp.Body.String())
		}
	}
}

func TestWorkflowCreateRunAndGet(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", Workflows: workflow.NewMemoryStore()}).Handler()

	defBody := `{"api_version":"2026-05-22","name":"demo","steps":[{"id":"s1","target":"mock","command":"submit","input":{"prompt":"hi"}}]}`
	create := doJSON(server, http.MethodPost, "/v1/workflows", defBody, authHeaders("wfdef-key-000000000001"))
	if create.Code != http.StatusCreated {
		t.Fatalf("create workflow = %d; body=%s", create.Code, create.Body.String())
	}
	var def workflowDefinitionResponse
	if err := json.Unmarshal(create.Body.Bytes(), &def); err != nil {
		t.Fatalf("decode workflow definition: %v", err)
	}
	if def.ID == "" {
		t.Fatalf("expected workflow definition id")
	}

	runResp := doJSON(server, http.MethodPost, "/v1/workflows/"+def.ID+"/runs", `{}`, authHeaders("wfrun-key-000000000001"))
	if runResp.Code != http.StatusAccepted {
		t.Fatalf("create run = %d; body=%s", runResp.Code, runResp.Body.String())
	}
	var run workflowRunResponse
	if err := json.Unmarshal(runResp.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode workflow run: %v", err)
	}
	if run.ID == "" {
		t.Fatalf("expected workflow run id")
	}

	got := doJSON(server, http.MethodGet, "/v1/workflows/runs/"+run.ID, "", authHeaders(""))
	if got.Code != http.StatusOK {
		t.Fatalf("get run = %d; body=%s", got.Code, got.Body.String())
	}
}

func TestWorkflowRejectsUnsafeStepInput(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", Workflows: workflow.NewMemoryStore()}).Handler()
	defBody := `{"api_version":"2026-05-22","name":"demo","steps":[{"id":"s1","target":"mock","command":"submit","input":{"password":"hunter2"}}]}`
	create := doJSON(server, http.MethodPost, "/v1/workflows", defBody, authHeaders("wfdef-key-000000000099"))
	if create.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsafe input; got %d; body=%s", create.Code, create.Body.String())
	}
}

func TestWorkflowNotImplementedWhenNil(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret"}).Handler()
	// GET list returns an empty collection even when no store is configured.
	list := doJSON(server, http.MethodGet, "/v1/workflows", "", authHeaders(""))
	if list.Code != http.StatusOK {
		t.Fatalf("list workflows = %d; body=%s", list.Code, list.Body.String())
	}
	// POST returns 501 when not configured.
	create := doJSON(server, http.MethodPost, "/v1/workflows", `{"name":"x","steps":[{"target":"mock","command":"submit"}]}`, authHeaders("wfdef-key-000000000002"))
	if create.Code != http.StatusNotImplemented {
		t.Fatalf("create workflow = %d; want 501; body=%s", create.Code, create.Body.String())
	}
}

func TestSCIMUserLifecycle(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", SCIM: scim.NewMemoryStore()}).Handler()

	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"alice@example.com","active":true}`
	create := doRaw(server, http.MethodPost, "/v1/scim/v2/Users", body, scim.ContentType, scimHeaders(""))
	if create.Code != http.StatusCreated {
		t.Fatalf("create scim user = %d; body=%s", create.Code, create.Body.String())
	}
	var user scim.User
	if err := json.Unmarshal(create.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode scim user: %v", err)
	}
	if user.ID == "" {
		t.Fatalf("expected scim user id")
	}

	get := doRaw(server, http.MethodGet, "/v1/scim/v2/Users/"+user.ID, "", scim.ContentType, scimHeaders(""))
	if get.Code != http.StatusOK {
		t.Fatalf("get scim user = %d; body=%s", get.Code, get.Body.String())
	}

	del := doRaw(server, http.MethodDelete, "/v1/scim/v2/Users/"+user.ID, "", scim.ContentType, scimHeaders(""))
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete scim user = %d; body=%s", del.Code, del.Body.String())
	}
}

func TestSCIMRequiresRoleManage(t *testing.T) {
	// Default "service" role lacks role:manage.
	server := NewServer(Config{AppSecret: "dev-secret", SCIM: scim.NewMemoryStore()}).Handler()
	resp := doRaw(server, http.MethodGet, "/v1/scim/v2/Users", "", scim.ContentType, scimHeaders(""))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("scim list with service role = %d; want 403; body=%s", resp.Code, resp.Body.String())
	}
}

func TestSSOConfigPutAndGet(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", SSO: sso.NewMemoryStore()}).Handler()

	put := doJSON(server, http.MethodPut, "/v1/sso/config", `{"type":"oidc","oidc":{"Issuer":"https://idp.example.com","ClientID":"abc"}}`, authHeaders(""))
	if put.Code != http.StatusOK {
		t.Fatalf("put sso config = %d; body=%s", put.Code, put.Body.String())
	}

	get := doJSON(server, http.MethodGet, "/v1/sso/config", "", authHeaders(""))
	if get.Code != http.StatusOK {
		t.Fatalf("get sso config = %d; body=%s", get.Code, get.Body.String())
	}
	var cfg ssoConfigResponse
	if err := json.Unmarshal(get.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode sso config: %v", err)
	}
	if len(cfg.OIDC) != 1 {
		t.Fatalf("expected 1 oidc config; got %d", len(cfg.OIDC))
	}
}

func TestSIEMConfigAndAuditExport(t *testing.T) {
	sinkPath := filepath.Join(t.TempDir(), "siem.log")
	exporter, err := siem.NewExporter(siem.ExporterConfig{Sinks: []siem.Sink{siem.NewFileSink(sinkPath)}})
	if err != nil {
		t.Fatalf("new exporter: %v", err)
	}
	server := NewServer(Config{
		AppSecret:    "dev-secret",
		ActorRole:    "admin",
		SIEMConfig:   siem.NewMemoryStore(),
		SIEMExporter: exporter,
	}).Handler()

	put := doJSON(server, http.MethodPut, "/v1/siem/config", `{"name":"primary","kind":"file","target":"/tmp/out.log","enabled":true}`, authHeaders(""))
	if put.Code != http.StatusOK {
		t.Fatalf("put siem config = %d; body=%s", put.Code, put.Body.String())
	}

	get := doJSON(server, http.MethodGet, "/v1/siem/config", "", authHeaders(""))
	if get.Code != http.StatusOK {
		t.Fatalf("get siem config = %d; body=%s", get.Code, get.Body.String())
	}

	export := doJSON(server, http.MethodPost, "/v1/audit/export", `{}`, authHeaders(""))
	if export.Code != http.StatusAccepted {
		t.Fatalf("audit export = %d; body=%s", export.Code, export.Body.String())
	}
}

func TestWebhookSecretRotateIdempotent(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin"}).Handler()

	body := `{"webhook_id":"wh_123","new_secret_ref":"vault://webhooks/wh_123/v2","overlap_seconds":60}`
	key := "rotate-key-0000000001"
	first := doJSON(server, http.MethodPost, "/v1/webhooks/secret:rotate", body, authHeaders(key))
	if first.Code != http.StatusOK {
		t.Fatalf("rotate = %d; body=%s", first.Code, first.Body.String())
	}
	var resp webhookSecretRotateResponse
	if err := json.Unmarshal(first.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode rotate response: %v", err)
	}
	if resp.ActiveSecretRef != "vault://webhooks/wh_123/v2" {
		t.Fatalf("unexpected active secret ref: %q", resp.ActiveSecretRef)
	}

	replay := doJSON(server, http.MethodPost, "/v1/webhooks/secret:rotate", body, authHeaders(key))
	if replay.Code != http.StatusOK {
		t.Fatalf("rotate replay = %d; body=%s", replay.Code, replay.Body.String())
	}
	var replayResp webhookSecretRotateResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &replayResp); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if replayResp.ActiveSecretRef != resp.ActiveSecretRef {
		t.Fatalf("replay active ref mismatch: %q vs %q", replayResp.ActiveSecretRef, resp.ActiveSecretRef)
	}
}

func TestWebhookSecretRotateRejectsPlaintextSecret(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin"}).Handler()
	body := `{"webhook_id":"wh_123","new_secret_ref":"ref","password":"hunter2"}`
	resp := doJSON(server, http.MethodPost, "/v1/webhooks/secret:rotate", body, authHeaders("rotate-key-0000000002"))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for plaintext secret; got %d; body=%s", resp.Code, resp.Body.String())
	}
}

func TestRateLimitsListRequiresManage(t *testing.T) {
	resolver := ratelimit.DefaultPolicyResolver()
	limiter := ratelimit.New(ratelimit.NewMemoryStore(), resolver.Default())
	server := NewServer(Config{
		AppSecret:         "dev-secret",
		ActorRole:         "admin",
		RateLimiter:       limiter,
		RateLimitResolver: resolver,
	}).Handler()
	resp := doJSON(server, http.MethodGet, "/v1/rate-limits", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("rate-limits list = %d; body=%s", resp.Code, resp.Body.String())
	}
	var status rateLimitStatusResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode rate-limits: %v", err)
	}
	if len(status.Policies) == 0 {
		t.Fatalf("expected at least one policy")
	}
}

func TestAuditExportStreamsPersistedChain(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", Audit: audit.NewMemoryStore()}).Handler()

	// The first export persists its own authorization audit record, which the
	// second export then reads back as part of the verified chain.
	if first := doJSON(server, http.MethodPost, "/v1/audit/export", `{}`, authHeaders("")); first.Code != http.StatusOK {
		t.Fatalf("first export = %d; body=%s", first.Code, first.Body.String())
	}
	second := doJSON(server, http.MethodPost, "/v1/audit/export", `{}`, authHeaders(""))
	if second.Code != http.StatusOK {
		t.Fatalf("second export = %d; body=%s", second.Code, second.Body.String())
	}
	var resp auditExportResponse
	if err := json.Unmarshal(second.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if !resp.ChainValid {
		t.Fatalf("expected chain_valid true; got %+v", resp)
	}
	if resp.HeadHash == "" || resp.Count < 1 || len(resp.Records) < 1 {
		t.Fatalf("expected persisted records with head hash; got %+v", resp)
	}
}

func TestAuditExportAcceptsSDKBodyAndSequenceWindow(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", Audit: audit.NewMemoryStore()}).Handler()

	// Warm the chain with a few authorization records.
	for i := 0; i < 3; i++ {
		if r := doJSON(server, http.MethodPost, "/v1/audit/export", `{}`, authHeaders("")); r.Code != http.StatusOK {
			t.Fatalf("warm export %d = %d; body=%s", i, r.Code, r.Body.String())
		}
	}

	// The SDKs always send idempotency_key (and may send a sequence range);
	// the handler must accept both despite DisallowUnknownFields, and the
	// sequence window must not break chain verification.
	body := `{"idempotency_key":"idem-123","range":{"from_sequence":2,"to_sequence":2}}`
	resp := doJSON(server, http.MethodPost, "/v1/audit/export", body, authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("export = %d; body=%s", resp.Code, resp.Body.String())
	}
	var decoded auditExportResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if !decoded.ChainValid {
		t.Fatalf("expected chain_valid true over full chain; got %+v", decoded)
	}
	for _, rec := range decoded.Records {
		if rec.Seq != 2 {
			t.Fatalf("sequence window not applied; got seq %d", rec.Seq)
		}
	}
}

func TestSSOLogoutRevokesSession(t *testing.T) {
	sessions := session.NewMemoryStore()
	now := time.Now().UTC()
	_, token, err := sessions.Create(context.Background(), session.Session{
		TenantID:  "tenant-default",
		Role:      "admin",
		Subject:   "user-1",
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", Sessions: sessions}).Handler()

	logout := doJSON(server, http.MethodPost, "/v1/sso/logout", "", map[string]string{
		"Authorization":    "Bearer " + token,
		"Ubag-Api-Version": DefaultAPIVersion,
	})
	if logout.Code != http.StatusOK {
		t.Fatalf("logout = %d; body=%s", logout.Code, logout.Body.String())
	}
	var resp ssoLogoutResponse
	if err := json.Unmarshal(logout.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode logout: %v", err)
	}
	if !resp.Revoked {
		t.Fatalf("expected revoked true; got %+v", resp)
	}
	if _, ok, _ := sessions.Resolve(context.Background(), token, time.Now()); ok {
		t.Fatalf("session should no longer resolve after logout")
	}

	// Logging out again (via the static app-secret principal) is idempotent.
	again := doJSON(server, http.MethodPost, "/v1/sso/logout", "", authHeaders(""))
	if again.Code != http.StatusOK {
		t.Fatalf("idempotent logout = %d; body=%s", again.Code, again.Body.String())
	}
	var againResp ssoLogoutResponse
	if err := json.Unmarshal(again.Body.Bytes(), &againResp); err != nil {
		t.Fatalf("decode idempotent logout: %v", err)
	}
	if againResp.Revoked {
		t.Fatalf("expected revoked false for app-secret logout; got %+v", againResp)
	}
}
