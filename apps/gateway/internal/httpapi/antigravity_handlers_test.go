package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/antigravity"
)

func TestAntigravityRoutesRequireAdminRole(t *testing.T) {
	operator := NewServer(Config{AppSecret: "dev-secret", ActorRole: "operator"}).Handler()
	headers := map[string]string{
		"Authorization":    "Bearer dev-secret",
		"Ubag-Api-Version": DefaultAPIVersion,
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/antigravity/accounts"},
		{http.MethodPost, "/v1/antigravity/accounts"},
		{http.MethodPut, "/v1/antigravity/accounts/acct_1"},
		{http.MethodDelete, "/v1/antigravity/accounts/acct_1"},
		{http.MethodGet, "/v1/antigravity/config"},
		{http.MethodPut, "/v1/antigravity/config"},
		{http.MethodPost, "/v1/antigravity/test"},
		{http.MethodGet, "/v1/quotas/antigravity"},
		{http.MethodPost, "/v1/quotas/antigravity/refresh"},
		{http.MethodGet, "/v1/quotas/antigravity/acct_1"},
	} {
		response := doJSON(operator, route.method, route.path, "{}", headers)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403; body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func TestAntigravityOAuthAccountNeverAcceptsAPIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	admin := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin"}).Handler()
	headers := map[string]string{
		"Authorization":    "Bearer dev-secret",
		"Ubag-Api-Version": DefaultAPIVersion,
	}
	response := doJSON(admin, http.MethodPost, "/v1/antigravity/accounts", `{"label":"OAuth profile","api_key":"test-secret-never-store"}`, headers)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
}

func TestAntigravityQuotaIsUnavailableWithoutVerifiedCollector(t *testing.T) {
	admin := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin"}).Handler()
	headers := map[string]string{
		"Authorization":    "Bearer dev-secret",
		"Ubag-Api-Version": DefaultAPIVersion,
	}
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/quotas/antigravity"},
		{http.MethodPost, "/v1/quotas/antigravity/refresh"},
		{http.MethodGet, "/v1/quotas/antigravity/acct_1"},
	} {
		response := doJSON(admin, route.method, route.path, "{}", headers)
		if response.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s status = %d, want 501; body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func TestAntigravityTestDoesNotQueueSDKJobWhenOAuthIsDisabled(t *testing.T) {
	t.Setenv("UBAG_ANTIGRAVITY_ENABLED", "false")
	dispatcher := &recordingExecutor{}
	admin := NewServer(Config{
		AppSecret: "dev-secret", ActorRole: "admin", Executor: dispatcher,
	}).Handler()
	headers := map[string]string{
		"Authorization":    "Bearer dev-secret",
		"Ubag-Api-Version": DefaultAPIVersion,
		"Idempotency-Key":  "antigravity-oauth-test-1",
	}
	response := doJSON(admin, http.MethodPost, "/v1/antigravity/test", `{"prompt":"test"}`, headers)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
	if len(dispatcher.enqueued) != 0 {
		t.Fatalf("OAuth test queued %d jobs while disabled", len(dispatcher.enqueued))
	}
}

func TestAntigravityAdminTestRoutesToOAuthCLI(t *testing.T) {
	t.Setenv("UBAG_ANTIGRAVITY_ENABLED", "true")
	dispatcher := &recordingExecutor{}
	admin := NewServer(Config{
		AppSecret: "dev-secret", ActorRole: "admin", Executor: dispatcher,
	}).Handler()
	headers := map[string]string{
		"Authorization":    "Bearer dev-secret",
		"Ubag-Api-Version": DefaultAPIVersion,
		"Idempotency-Key":  "antigravity-cli-routing-test-1",
	}
	response := doJSON(admin, http.MethodPost, "/v1/antigravity/test", `{"prompt":"test"}`, headers)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", response.Code, response.Body.String())
	}
	if len(dispatcher.enqueued) != 1 || dispatcher.enqueued[0].Target != "antigravity_cli" {
		t.Fatalf("expected one OAuth CLI job, got %#v", dispatcher.enqueued)
	}
}

func TestAntigravityOAuthAccountsAreTenantScoped(t *testing.T) {
	dataDir := t.TempDir()
	store := antigravity.NewStore(dataDir)
	adminA := NewServer(Config{
		AppSecret: "dev-secret", ActorRole: "admin", TenantID: "tenant_a", AntigravityStore: store,
	}).Handler()
	adminB := NewServer(Config{
		AppSecret: "dev-secret", ActorRole: "admin", TenantID: "tenant_b", AntigravityStore: store,
	}).Handler()
	headers := map[string]string{
		"Authorization":    "Bearer dev-secret",
		"Ubag-Api-Version": DefaultAPIVersion,
	}
	created := doJSON(adminA, http.MethodPost, "/v1/antigravity/accounts", `{"label":"Primary","tier":"pro"}`, headers)
	if created.Code != http.StatusCreated {
		t.Fatalf("create OAuth slot status = %d, want 201; body=%s", created.Code, created.Body.String())
	}
	var account struct {
		ID string `json:"account_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &account); err != nil {
		t.Fatalf("decode created account: %v", err)
	}
	other := doJSON(adminB, http.MethodGet, "/v1/antigravity/accounts", "", headers)
	var body struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(other.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode account list: %v", err)
	}
	if other.Code != http.StatusOK || len(body.Accounts) != 0 {
		t.Fatalf("another tenant can see OAuth account: status=%d body=%s", other.Code, other.Body.String())
	}
	path := "/v1/antigravity/accounts/" + account.ID
	ownerUpdate := doJSON(adminA, http.MethodPut, path, `{"enabled":false}`, headers)
	if ownerUpdate.Code != http.StatusOK {
		t.Fatalf("owner cannot update OAuth account: status=%d body=%s", ownerUpdate.Code, ownerUpdate.Body.String())
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		response := doJSON(adminB, method, path, `{"enabled":false}`, headers)
		if response.Code != http.StatusNotFound {
			t.Fatalf("another tenant can %s OAuth account: status=%d body=%s", method, response.Code, response.Body.String())
		}
	}
	owner := doJSON(adminA, http.MethodGet, "/v1/antigravity/accounts", "", headers)
	if err := json.Unmarshal(owner.Body.Bytes(), &body); err != nil || len(body.Accounts) != 1 || body.Accounts[0]["enabled"] != false {
		t.Fatalf("foreign mutations changed owner account: status=%d body=%s err=%v", owner.Code, owner.Body.String(), err)
	}
	if body.Accounts[0]["worker_socket_present"] != false {
		t.Fatalf("unprovisioned OAuth worker reported as present: %s", owner.Body.String())
	}
	if accounts := antigravity.NewStore(dataDir).ListAccounts("tenant_a"); len(accounts) != 1 || accounts[0].Enabled {
		t.Fatalf("owner account update did not persist: %#v", accounts)
	}
	removed := doJSON(adminA, http.MethodDelete, path, "", headers)
	if removed.Code != http.StatusOK {
		t.Fatalf("owner cannot remove OAuth account: status=%d body=%s", removed.Code, removed.Body.String())
	}
}

func TestAntigravityOAuthAccountLimitAndTenantCount(t *testing.T) {
	store := antigravity.NewStore(t.TempDir())
	adminA := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", TenantID: "tenant_a", AntigravityStore: store}).Handler()
	adminB := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", TenantID: "tenant_b", AntigravityStore: store}).Handler()
	headers := map[string]string{"Authorization": "Bearer dev-secret", "Ubag-Api-Version": DefaultAPIVersion}
	for number := 1; number <= 3; number++ {
		response := doJSON(adminA, http.MethodPost, "/v1/antigravity/accounts", `{"label":"OAuth","tier":"pro"}`, headers)
		if response.Code != http.StatusCreated {
			t.Fatalf("slot %d status = %d; body=%s", number, response.Code, response.Body.String())
		}
	}
	full := doJSON(adminA, http.MethodPost, "/v1/antigravity/accounts", `{"label":"Fourth"}`, headers)
	if full.Code != http.StatusConflict {
		t.Fatalf("fourth OAuth slot status = %d, want 409; body=%s", full.Code, full.Body.String())
	}
	config := doJSON(adminB, http.MethodGet, "/v1/antigravity/config", "", headers)
	var response struct {
		AccountCount int `json:"account_count"`
	}
	if err := json.Unmarshal(config.Body.Bytes(), &response); err != nil || response.AccountCount != 0 {
		t.Fatalf("tenant B sees another tenant's slot count: status=%d body=%s err=%v", config.Code, config.Body.String(), err)
	}
}

func TestAntigravityCanaryTargetsOnlyAnOwnedOAuthAccount(t *testing.T) {
	t.Setenv("UBAG_ANTIGRAVITY_ENABLED", "true")
	store := antigravity.NewStore(t.TempDir())
	owner, err := store.AddAccount("tenant_a", "Primary", "pro")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.AddAccount("tenant_b", "Other", "pro")
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &recordingExecutor{}
	admin := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", TenantID: "tenant_a", AntigravityStore: store, Executor: dispatcher}).Handler()
	headers := map[string]string{
		"Authorization": "Bearer dev-secret", "Ubag-Api-Version": DefaultAPIVersion,
		"Idempotency-Key": "antigravity-owned-canary",
	}
	foreign := doJSON(admin, http.MethodPost, "/v1/antigravity/test", fmt.Sprintf(`{"prompt":"canary","account_id":%q}`, other.ID), headers)
	if foreign.Code != http.StatusNotFound || len(dispatcher.enqueued) != 0 {
		t.Fatalf("foreign account canary status=%d jobs=%d body=%s", foreign.Code, len(dispatcher.enqueued), foreign.Body.String())
	}
	owned := doJSON(admin, http.MethodPost, "/v1/antigravity/test", fmt.Sprintf(`{"prompt":"canary","account_id":%q}`, owner.ID), headers)
	if owned.Code != http.StatusAccepted || len(dispatcher.enqueued) != 1 {
		t.Fatalf("owned canary status=%d jobs=%d body=%s", owned.Code, len(dispatcher.enqueued), owned.Body.String())
	}
	if got := dispatcher.enqueued[0].Options["antigravity_account_id"]; got != owner.ID {
		t.Fatalf("canary did not pin the OAuth account: %#v", dispatcher.enqueued[0].Options)
	}
}
