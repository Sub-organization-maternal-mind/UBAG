package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/antigravity"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
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
		{http.MethodGet, "/v1/antigravity/accounts/acct_1/login"},
		{http.MethodPost, "/v1/antigravity/accounts/acct_1/login"},
		{http.MethodDelete, "/v1/antigravity/accounts/acct_1/login"},
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

func TestAntigravityLoginRequiresOwnedProvisionedWorker(t *testing.T) {
	t.Setenv("UBAG_ANTIGRAVITY_ENABLED", "false")
	t.Setenv("UBAG_ANTIGRAVITY_SOCKET_DIR", t.TempDir())
	store := antigravity.NewStore(t.TempDir())
	owned, err := store.AddAccount("tenant_a", "Primary", "pro")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := store.AddAccount("tenant_b", "Other", "pro")
	if err != nil {
		t.Fatal(err)
	}
	admin := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", TenantID: "tenant_a", AntigravityStore: store}).Handler()
	headers := map[string]string{"Authorization": "Bearer dev-secret", "Ubag-Api-Version": DefaultAPIVersion}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		body := ""
		if method == http.MethodPost {
			body = `{"action":"start"}`
		}
		other := doJSON(admin, method, "/v1/antigravity/accounts/"+foreign.ID+"/login", body, headers)
		if other.Code != http.StatusNotFound {
			t.Fatalf("%s foreign login status=%d body=%s", method, other.Code, other.Body.String())
		}
		missing := doJSON(admin, method, "/v1/antigravity/accounts/"+owned.ID+"/login", body, headers)
		if missing.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s missing worker login status=%d body=%s", method, missing.Code, missing.Body.String())
		}
	}
}

func TestAntigravityLoginRelaysCodeToOwnedWorker(t *testing.T) {
	t.Setenv("UBAG_ANTIGRAVITY_ENABLED", "false")
	store := antigravity.NewStore(t.TempDir())
	account, err := store.AddAccount("tenant_a", "Primary", "pro")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetVerificationJob("tenant_a", account.ID, "job_previous"); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("", "ag-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	t.Setenv("UBAG_ANTIGRAVITY_SOCKET_DIR", socketDir)
	path := antigravity.AccountSocketPath(socketDir, account.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	requests := make(chan map[string]any, 4)
	go func() {
		polls := 0
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			var request map[string]any
			if json.NewDecoder(connection).Decode(&request) == nil {
				requests <- request
				switch request["operation"] {
				case "login_start":
					fmt.Fprintln(connection, `{"state":"awaiting_code","authorization_url":"https://accounts.google.com/o/oauth2/auth?state=synthetic"}`)
				case "login_input":
					fmt.Fprintln(connection, `{"state":"verifying"}`)
				case "login_poll":
					polls++
					if polls == 1 {
						fmt.Fprintln(connection, `{"state":"verifying"}`)
					} else {
						fmt.Fprintln(connection, `{"state":"verifying","output":"synthetic-private-code"}`)
					}
				case "login_stop":
					fmt.Fprintln(connection, `{"state":"stopped"}`)
				}
			}
			connection.Close()
		}
	}()

	admin := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", TenantID: "tenant_a", AntigravityStore: store}).Handler()
	headers := map[string]string{"Authorization": "Bearer dev-secret", "Ubag-Api-Version": DefaultAPIVersion}
	path = "/v1/antigravity/accounts/" + account.ID + "/login"
	disabled := doJSON(admin, http.MethodPost, path, `{"action":"start"}`, headers)
	if disabled.Code != http.StatusServiceUnavailable || store.ListAccounts("tenant_a")[0].VerificationJobID != "job_previous" {
		t.Fatalf("disabled OAuth started login or cleared verification: status=%d", disabled.Code)
	}
	select {
	case request := <-requests:
		t.Fatalf("disabled login reached the account worker: %#v", request)
	default:
	}
	t.Setenv("UBAG_ANTIGRAVITY_ENABLED", "true")
	for _, request := range []struct {
		method, body, operation string
	}{
		{http.MethodPost, `{"action":"start"}`, "login_start"},
		{http.MethodPost, `{"action":"input","input":"synthetic-code"}`, "login_input"},
		{http.MethodGet, "", "login_poll"},
		{http.MethodDelete, "", "login_stop"},
	} {
		response := doJSON(admin, request.method, path, request.body, headers)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "synthetic-code") {
			t.Fatalf("%s %s status=%d body=%s", request.method, request.operation, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("login response may be cached: %s", response.Header().Get("Cache-Control"))
		}
		got := <-requests
		if got["operation"] != request.operation {
			t.Fatalf("worker operation=%v, want %s", got, request.operation)
		}
		if request.operation == "login_input" && got["input"] != "synthetic-code" {
			t.Fatalf("worker did not receive only the one-time code: %#v", got)
		}
		if request.operation == "login_start" {
			if accounts := store.ListAccounts("tenant_a"); len(accounts) != 1 || accounts[0].VerificationJobID != "" {
				t.Fatalf("starting sign-in retained old verification: %#v", accounts)
			}
		}
	}
	for _, body := range []string{
		`{"action":"input","input":"/logout"}`,
		`{"action":"input","input":"` + strings.Repeat("x", 129) + `"}`,
		`{"action":"input","input":"synthetic-code","access_token":"should-reject"}`,
	} {
		response := doJSON(admin, http.MethodPost, path, body, headers)
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "should-reject") {
			t.Fatalf("unsafe login input status=%d body=%s", response.Code, response.Body.String())
		}
	}
	select {
	case unexpected := <-requests:
		t.Fatalf("invalid input reached account worker: %#v", unexpected)
	default:
	}
	unexpectedOutput := doJSON(admin, http.MethodGet, path, "", headers)
	if unexpectedOutput.Code != http.StatusBadGateway || strings.Contains(unexpectedOutput.Body.String(), "synthetic-private-code") {
		t.Fatalf("raw CLI output escaped the worker: status=%d body=%s", unexpectedOutput.Code, unexpectedOutput.Body.String())
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
	t.Setenv("UBAG_ANTIGRAVITY_ENABLED", "false")
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
		AccountCount int  `json:"account_count"`
		OAuthEnabled bool `json:"oauth_enabled"`
	}
	if err := json.Unmarshal(config.Body.Bytes(), &response); err != nil || response.AccountCount != 0 || response.OAuthEnabled {
		t.Fatalf("tenant B sees another tenant's slot count: status=%d body=%s err=%v", config.Code, config.Body.String(), err)
	}
	t.Setenv("UBAG_ANTIGRAVITY_ENABLED", "true")
	config = doJSON(adminB, http.MethodGet, "/v1/antigravity/config", "", headers)
	if err := json.Unmarshal(config.Body.Bytes(), &response); err != nil || !response.OAuthEnabled {
		t.Fatalf("OAuth feature switch not reported: status=%d body=%s err=%v", config.Code, config.Body.String(), err)
	}
}

type canaryEnqueueObserver struct {
	*recordingExecutor
	accounts            *antigravity.Store
	registeredAtEnqueue bool
}

func (observer *canaryEnqueueObserver) EnqueueJob(ctx context.Context, job jobstore.Job) (executor.Receipt, error) {
	for _, account := range observer.accounts.ListAccounts(job.TenantID) {
		if account.ID == job.Options["antigravity_account_id"] && account.VerificationJobID == job.ID {
			observer.registeredAtEnqueue = true
		}
	}
	return observer.recordingExecutor.EnqueueJob(ctx, job)
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
	dispatcher := &canaryEnqueueObserver{recordingExecutor: &recordingExecutor{}, accounts: store}
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
	if !dispatcher.registeredAtEnqueue {
		t.Fatal("the pinned canary was enqueued before its verification ID was stored")
	}
}

func TestAntigravityAccountVerificationFollowsLatestPinnedCanary(t *testing.T) {
	t.Setenv("UBAG_ANTIGRAVITY_ENABLED", "true")
	dataDir := t.TempDir()
	store := antigravity.NewStore(dataDir)
	account, err := store.AddAccount("tenant_a", "Primary", "pro")
	if err != nil {
		t.Fatal(err)
	}
	jobs := jobstore.NewMemoryStore()
	admin := NewServer(Config{
		AppSecret: "dev-secret", ActorRole: "admin", TenantID: "tenant_a", AntigravityStore: store,
		Jobs: jobs, Executor: &recordingExecutor{},
	}).Handler()
	headers := map[string]string{
		"Authorization": "Bearer dev-secret", "Ubag-Api-Version": DefaultAPIVersion,
		"Idempotency-Key": "antigravity-canary-first",
	}
	state := func() (string, string, string) {
		t.Helper()
		response := doJSON(admin, http.MethodGet, "/v1/antigravity/accounts", "", headers)
		var payload struct {
			Accounts []struct {
				State      string `json:"verification_state"`
				JobID      string `json:"verification_job_id"`
				VerifiedAt string `json:"verified_at"`
			} `json:"accounts"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || len(payload.Accounts) != 1 {
			t.Fatalf("account state status=%d body=%s", response.Code, response.Body.String())
		}
		if payload.Accounts[0].State != "verified" && strings.Contains(response.Body.String(), `"verified_at"`) {
			t.Fatalf("unverified account should not publish verified_at: %s", response.Body.String())
		}
		return payload.Accounts[0].State, payload.Accounts[0].JobID, payload.Accounts[0].VerifiedAt
	}
	if current, _, _ := state(); current != "unverified" {
		t.Fatalf("new account verification=%q, want unverified", current)
	}
	canary := func() string {
		t.Helper()
		response := doJSON(admin, http.MethodPost, "/v1/antigravity/test", fmt.Sprintf(`{"prompt":"Reply with OK.","account_id":%q}`, account.ID), headers)
		var result struct {
			JobID string `json:"job_id"`
		}
		if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.JobID == "" {
			t.Fatalf("pinned canary status=%d body=%s", response.Code, response.Body.String())
		}
		return result.JobID
	}
	first := canary()
	if current, jobID, _ := state(); current != "pending" || jobID != first {
		t.Fatalf("accepted job verification=%q job=%q, want pending %q", current, jobID, first)
	}
	if _, found, err := jobs.UpdateStatus(context.Background(), first, jobstore.StatusCompleted); !found || err != nil {
		t.Fatalf("complete first canary: found=%v err=%v", found, err)
	}
	if current, _, verifiedAt := state(); current != "verified" || verifiedAt == "" {
		t.Fatalf("completed canary verification=%q verified_at=%q", current, verifiedAt)
	}
	headers["Idempotency-Key"] = "antigravity-canary-second"
	second := canary()
	if current, jobID, _ := state(); current != "pending" || jobID != second {
		t.Fatalf("new canary verification=%q job=%q, want pending %q", current, jobID, second)
	}
	if _, found, err := jobs.UpdateStatus(context.Background(), second, jobstore.StatusFailedTerminal); !found || err != nil {
		t.Fatalf("fail second canary: found=%v err=%v", found, err)
	}
	if current, _, verifiedAt := state(); current != "failed" || verifiedAt != "" {
		t.Fatalf("failed canary verification=%q verified_at=%q", current, verifiedAt)
	}
	loaded := antigravity.NewStore(dataDir).ListAccounts("tenant_a")
	if len(loaded) != 1 {
		t.Fatalf("account metadata was not persisted: %#v", loaded)
	}
	encoded, err := json.Marshal(loaded[0])
	if err != nil || !strings.Contains(string(encoded), `"verification_job_id":"`+second+`"`) {
		t.Fatalf("latest verification job was not persisted: %s err=%v", encoded, err)
	}
}

func TestAntigravityAccountVerificationAcceptsRequesterAppID(t *testing.T) {
	store := antigravity.NewStore(t.TempDir())
	account, err := store.AddAccount("tenant_a", "Primary", "pro")
	if err != nil {
		t.Fatal(err)
	}
	jobs := jobstore.NewMemoryStore()
	canary, err := jobs.Create(context.Background(), jobstore.CreateRequest{
		TenantID: "tenant_a", AppID: "tenant-app", Client: map[string]any{"app_id": "antigravity-test"},
		Target: "antigravity_cli", Options: map[string]any{"antigravity_account_id": account.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := jobs.UpdateStatus(context.Background(), canary.ID, jobstore.StatusCompleted); err != nil || !found {
		t.Fatalf("complete mismatched canary: found=%v err=%v", found, err)
	}
	if err := store.SetVerificationJob("tenant_a", account.ID, canary.ID); err != nil {
		t.Fatal(err)
	}
	admin := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin", TenantID: "tenant_a", AntigravityStore: store, Jobs: jobs}).Handler()
	response := doJSON(admin, http.MethodGet, "/v1/antigravity/accounts", "", map[string]string{
		"Authorization": "Bearer dev-secret", "Ubag-Api-Version": DefaultAPIVersion,
	})
	var payload struct {
		Accounts []struct {
			State string `json:"verification_state"`
		} `json:"accounts"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || len(payload.Accounts) != 1 {
		t.Fatalf("account list status=%d body=%s", response.Code, response.Body.String())
	}
	if payload.Accounts[0].State != "verified" {
		t.Fatalf("requester app's completed canary was reported as %q", payload.Accounts[0].State)
	}
}
