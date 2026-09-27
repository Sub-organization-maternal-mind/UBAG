package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/antigravity"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

var antigravityLoginCode = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

func (s *Server) handleAntigravityAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listAntigravityAccounts(w, r)
	case http.MethodPost:
		s.addAntigravityAccount(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleAntigravityAccountByID(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	accountID := r.PathValue("id")
	if accountID == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-001", "account id is required"))
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.removeAntigravityAccount(w, r, accountID)
	case http.MethodPut:
		s.updateAntigravityAccount(w, r, accountID)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodPut, http.MethodDelete)
	}
}

func (s *Server) handleAntigravityLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPost, http.MethodDelete)
		return
	}

	tenantID, _ := requestScope(r)
	accountID := r.PathValue("id")
	owned := false
	for _, account := range s.antigravityAccountStore().ListAccounts(tenantID) {
		if account.ID == accountID {
			owned = true
			break
		}
	}
	if !owned {
		s.writeError(w, r, http.StatusNotFound, validationError("UBAG-ANTIGRAVITY-008", "account not found"))
		return
	}
	if !antigravity.OAuthEnabled() {
		s.writeError(w, r, http.StatusServiceUnavailable, targetError("UBAG-ANTIGRAVITY-LOGIN-004", "Antigravity OAuth is disabled", false, nil))
		return
	}

	socketDir := os.Getenv("UBAG_ANTIGRAVITY_SOCKET_DIR")
	if socketDir == "" || filepath.Base(accountID) != accountID || accountID == "." {
		s.writeError(w, r, http.StatusServiceUnavailable, targetError("UBAG-ANTIGRAVITY-LOGIN-001", "isolated account worker is unavailable", true, nil))
		return
	}
	info, err := os.Lstat(antigravity.AccountSocketPath(socketDir, accountID))
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		s.writeError(w, r, http.StatusServiceUnavailable, targetError("UBAG-ANTIGRAVITY-LOGIN-001", "isolated account worker is unavailable", true, nil))
		return
	}
	request := map[string]string{}
	switch r.Method {
	case http.MethodGet:
		request["operation"] = "login_poll"
	case http.MethodDelete:
		request["operation"] = "login_stop"
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1025))
		if err != nil || len(body) > 1024 {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-LOGIN-003", "invalid login request"))
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		var input struct {
			Action string `json:"action"`
			Input  string `json:"input"`
		}
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-LOGIN-003", "invalid login request"))
			return
		}
		if input.Action == "start" && input.Input == "" {
			request["operation"] = "login_start"
		} else if input.Action == "input" && antigravityLoginCode.MatchString(input.Input) {
			request["operation"] = "login_input"
			request["input"] = input.Input
		} else {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-LOGIN-003", "invalid login request"))
			return
		}
	}
	if request["operation"] == "login_start" {
		if err := s.antigravityAccountStore().SetVerificationJob(tenantID, accountID, ""); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to reset account verification"))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", antigravity.AccountSocketPath(socketDir, accountID))
	if err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, targetError("UBAG-ANTIGRAVITY-LOGIN-001", "isolated account worker is unavailable", true, nil))
		return
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(15 * time.Second)); err != nil || json.NewEncoder(connection).Encode(request) != nil {
		s.writeError(w, r, http.StatusBadGateway, targetError("UBAG-ANTIGRAVITY-LOGIN-002", "account worker login request failed", true, nil))
		return
	}
	line, err := bufio.NewReader(io.LimitReader(connection, 8193)).ReadBytes('\n')
	if err != nil || len(line) > 8192 {
		s.writeError(w, r, http.StatusBadGateway, targetError("UBAG-ANTIGRAVITY-LOGIN-002", "account worker login request failed", true, nil))
		return
	}
	var response struct {
		State            string `json:"state"`
		AuthorizationURL string `json:"authorization_url,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&response) != nil || !validAntigravityLoginState(response.State) ||
		(response.AuthorizationURL != "" && (response.State != "awaiting_code" || !validAntigravityLoginURL(response.AuthorizationURL))) {
		s.writeError(w, r, http.StatusBadGateway, targetError("UBAG-ANTIGRAVITY-LOGIN-002", "account worker login request failed", true, nil))
		return
	}
	s.writeJSON(w, http.StatusOK, response)
}

func validAntigravityLoginState(state string) bool {
	switch state {
	case "not_started", "starting", "awaiting_code", "verifying", "closed", "stopped":
		return true
	default:
		return false
	}
}

func validAntigravityLoginURL(raw string) bool {
	if len(raw) > 4096 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" ||
		(parsed.Port() != "" && parsed.Port() != "443") {
		return false
	}
	host := parsed.Hostname()
	if host != "accounts.google.com" && host != "antigravity.google" && !strings.HasSuffix(host, ".antigravity.google") {
		return false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return false
	}
	for name := range query {
		switch strings.ToLower(name) {
		case "code", "access_token", "refresh_token", "id_token", "client_secret", "token":
			return false
		}
	}
	return true
}

func (s *Server) handleAntigravityConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getAntigravityConfig(w, r)
	case http.MethodPut:
		s.updateAntigravityConfig(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPut)
	}
}

func (s *Server) handleAntigravityTest(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if !antigravity.OAuthEnabled() {
		s.writeError(w, r, http.StatusServiceUnavailable, targetError("UBAG-ANTIGRAVITY-OAUTH-001", "Antigravity OAuth CLI is disabled", false, nil))
		return
	}

	// Bounded read: reject oversize bodies with 413 via readBody instead of
	// streaming r.Body unbounded into the decoder.
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var req struct {
		Prompt    string `json:"prompt"`
		Model     string `json:"model,omitempty"`
		AccountID string `json:"account_id,omitempty"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-002", "request body must be valid JSON"))
		return
	}
	if req.Prompt == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-003", "prompt is required"))
		return
	}

	store := s.antigravityAccountStore()
	if req.AccountID != "" {
		tenantID, _ := requestScope(r)
		found := false
		for _, account := range store.ListAccounts(tenantID) {
			if account.ID == req.AccountID {
				found = true
				if !account.Enabled {
					s.writeError(w, r, http.StatusConflict, validationError("UBAG-ANTIGRAVITY-013", "OAuth account is disabled"))
					return
				}
				break
			}
		}
		if !found {
			s.writeError(w, r, http.StatusNotFound, validationError("UBAG-ANTIGRAVITY-008", "account not found"))
			return
		}
	}
	cfg := store.GetConfig()
	model := req.Model
	if model == "" {
		model = cfg.DefaultModel
	}

	jobReq := createJobRequest{
		APIVersion: DefaultAPIVersion,
		Client: clientRequest{
			AppID:      "antigravity-test",
			AppVersion: "0.1.0",
			SDK:        sdkRequest{Name: "ubag-antigravity", Version: "0.1.0"},
		},
		Job: jobRequest{
			Target:        "antigravity_cli",
			CommandType:   "chat.prompt",
			Input:         map[string]any{"prompt": req.Prompt},
			ModelSettings: map[string]any{"model": model, "effort": cfg.DefaultEffort},
		},
	}
	if req.AccountID != "" {
		jobReq.Job.Options = map[string]any{"antigravity_account_id": req.AccountID}
	}

	prepared, ok := s.prepareCreateJob(w, r, jobReq)
	if !ok {
		return
	}

	requestHash, err := canonicalCreateJobHash(prepared.apiVersion, prepared.request)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JSON-001", "request body must be valid JSON"))
		return
	}

	scope := idempotencyScope(prepared)
	reservation := s.newJobReservation(scope, prepared.tenantID, prepared.request.Job.Target, prepared.appID)

	decision, err := s.idempotency.Reserve(r.Context(), scope, requestHash)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to reserve idempotency key"))
		return
	}

	switch decision.Kind {
	case "conflict":
		s.writeError(w, r, http.StatusConflict, validationError("UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001", "idempotency key was replayed with a different payload"))
		return
	case "replay":
		s.replayJob(w, r, decision.Record)
		return
	}

	if s.maxQueueDepth > 0 {
		stats, err := s.executor.Stats(r.Context())
		if err == nil {
			pending := 0
			for _, v := range stats.DepthByState {
				pending += v
			}
			if pending >= s.maxQueueDepth {
				reservation.release(r.Context())
				w.Header().Set("Retry-After", "30")
				errObj := queueError("UBAG-QUEUE-BACKPRESSURE-002", "queue is too deep; retry later", true)
				errObj.RetryAfterMS = ptrInt(30000)
				s.writeError(w, r, http.StatusTooManyRequests, errObj)
				return
			}
		}
	}

	if s.concurrency != nil {
		if !s.concurrency.Acquire(prepared.tenantID, prepared.request.Job.Target, prepared.appID) {
			reservation.release(r.Context())
			s.writeError(w, r, http.StatusTooManyRequests, concurrencyError("UBAG-CONCURRENCY-001", "concurrency ceiling reached for this target", nil))
			return
		}
		reservation.tokenAcquired = true
	}

	job, err := s.jobs.Create(r.Context(), jobstore.CreateRequest{
		APIVersion:     prepared.apiVersion,
		TenantID:       prepared.tenantID,
		AppID:          prepared.appID,
		IdempotencyKey: prepared.idempotencyKey,
		Target:         strings.TrimSpace(prepared.request.Job.Target),
		CommandType:    strings.TrimSpace(prepared.request.Job.CommandType),
		Client:         clientToMap(prepared.request.Client),
		Input:          prepared.request.Job.Input,
		Options:        optionsWithProviderConfig(prepared.request.Job.Options, prepared.request.Job.ModelSettings),
		Callbacks:      prepared.request.Job.Callbacks,
		Context:        prepared.request.Job.Context,
		TraceID:        traceIDFromContext(r.Context()),
		NotBefore:      prepared.request.Job.NotBefore,
	})
	if err != nil {
		reservation.fail(r.Context())
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to create job"))
		return
	}

	reservation.attachJob(job.ID)
	s.markConcurrencyAcquired(job.ID, prepared.tenantID, prepared.request.Job.Target, prepared.appID)

	if req.AccountID != "" {
		if err := store.SetVerificationJob(prepared.tenantID, req.AccountID, job.ID); err != nil {
			reservation.fail(r.Context())
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to record account canary"))
			return
		}
	}

	if _, err := s.executor.EnqueueJob(r.Context(), job); err != nil {
		reservation.fail(r.Context())
		s.writeError(w, r, http.StatusInternalServerError, queueError("UBAG-ANTIGRAVITY-004", "failed to enqueue test job", true))
		return
	}

	if err := s.idempotency.Complete(r.Context(), scope, job.ID, http.StatusAccepted); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to complete idempotency record"))
		return
	}
	w.Header().Set("Location", "/v1/jobs/"+job.ID)
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":  job.ID,
		"status":  "accepted",
		"model":   model,
		"message": "Antigravity test job created",
	})
}

func idempotencyScope(p preparedCreateJob) idempotency.Scope {
	return idempotency.Scope{
		TenantID:  p.tenantID,
		AppID:     p.appID,
		Operation: "create_job",
		Key:       p.idempotencyKey,
	}
}

func (s *Server) antigravityAccountStore() *antigravity.Store {
	if s.antigravityStore != nil {
		return s.antigravityStore
	}
	return antigravity.GetStore()
}

func (s *Server) listAntigravityAccounts(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := requestScope(r)
	accounts := s.antigravityAccountStore().ListAccounts(tenantID)

	resp := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		workerSocketPresent := false
		if socketDir := os.Getenv("UBAG_ANTIGRAVITY_SOCKET_DIR"); socketDir != "" && filepath.Base(a.ID) == a.ID && a.ID != "." {
			if info, err := os.Lstat(antigravity.AccountSocketPath(socketDir, a.ID)); err == nil && info.Mode()&os.ModeSocket != 0 {
				workerSocketPresent = true
			}
		}
		verificationState := "unverified"
		var verificationJobID string
		var verifiedAt *time.Time
		if a.VerificationJobID != "" {
			job, found, err := s.jobs.Get(r.Context(), a.VerificationJobID)
			if err != nil {
				s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read account verification"))
				return
			}
			if found && job.TenantID == tenantID && job.Client["app_id"] == "antigravity-test" && job.Target == "antigravity_cli" && job.Options["antigravity_account_id"] == a.ID {
				verificationJobID = job.ID
				switch {
				case job.Status == jobstore.StatusCompleted:
					verificationState = "verified"
					verifiedAt = &job.UpdatedAt
				case jobstore.TerminalStatus(job.Status):
					verificationState = "failed"
				default:
					verificationState = "pending"
				}
			}
		}
		accountDetails := map[string]any{
			"account_id":            a.ID,
			"label":                 a.Label,
			"enabled":               a.Enabled,
			"tier":                  a.Tier,
			"last_used":             a.LastUsed,
			"cooldown_until":        a.CooldownUntil,
			"created_at":            a.CreatedAt,
			"worker_socket_present": workerSocketPresent,
			"verification_state":    verificationState,
		}
		if verificationJobID != "" {
			accountDetails["verification_job_id"] = verificationJobID
		}
		if verifiedAt != nil {
			accountDetails["verified_at"] = verifiedAt
		}
		resp = append(resp, accountDetails)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"accounts": resp})
}

func (s *Server) addAntigravityAccount(w http.ResponseWriter, r *http.Request) {
	// Bounded read: reject oversize bodies with 413 via readBody.
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var req struct {
		Label  string `json:"label"`
		Tier   string `json:"tier"`
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-005", "request body must be valid JSON"))
		return
	}
	if req.APIKey != "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-007", "OAuth accounts cannot accept API keys; complete sign-in through an authorized account session"))
		return
	}
	if req.Label == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-006", "label is required"))
		return
	}
	tier := req.Tier
	if tier == "" {
		tier = "free"
	}

	tenantID, _ := requestScope(r)
	account, err := s.antigravityAccountStore().AddAccount(tenantID, req.Label, tier)
	if err != nil {
		if errors.Is(err, antigravity.ErrAccountLimit) {
			s.writeError(w, r, http.StatusConflict, validationError("UBAG-ANTIGRAVITY-012", "tenant OAuth account limit reached"))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to add OAuth account"))
		return
	}

	s.writeJSON(w, http.StatusCreated, map[string]any{
		"account_id": account.ID,
		"label":      account.Label,
		"enabled":    account.Enabled,
		"tier":       account.Tier,
		"created_at": account.CreatedAt,
	})
}

func (s *Server) removeAntigravityAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	tenantID, _ := requestScope(r)
	if err := s.antigravityAccountStore().RemoveAccount(tenantID, accountID); err != nil {
		// Fixed message: the underlying store error can carry internal detail.
		s.writeError(w, r, http.StatusNotFound, validationError("UBAG-ANTIGRAVITY-008", "account not found"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"removed": accountID})
}

func (s *Server) updateAntigravityAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	// Bounded read: reject oversize bodies with 413 via readBody.
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled *bool  `json:"enabled"`
		Label   string `json:"label"`
		Tier    string `json:"tier"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-009", "request body must be valid JSON"))
		return
	}

	tenantID, _ := requestScope(r)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	account, err := s.antigravityAccountStore().UpdateAccount(tenantID, accountID, enabled)
	if err != nil {
		// Fixed message: the underlying store error can carry internal detail.
		s.writeError(w, r, http.StatusNotFound, validationError("UBAG-ANTIGRAVITY-010", "account not found"))
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"account_id": account.ID,
		"label":      account.Label,
		"enabled":    account.Enabled,
		"tier":       account.Tier,
	})
}

func (s *Server) getAntigravityConfig(w http.ResponseWriter, r *http.Request) {
	store := s.antigravityAccountStore()
	cfg := store.GetConfig()
	tenantID, _ := requestScope(r)
	cfg.AccountCount = len(store.ListAccounts(tenantID))
	s.writeJSON(w, http.StatusOK, struct {
		antigravity.Config
		OAuthEnabled bool `json:"oauth_enabled"`
	}{cfg, antigravity.OAuthEnabled()})
}

func (s *Server) updateAntigravityConfig(w http.ResponseWriter, r *http.Request) {
	// Bounded read: reject oversize bodies with 413 via readBody.
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var req antigravity.Config
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-ANTIGRAVITY-011", "request body must be valid JSON"))
		return
	}

	store := s.antigravityAccountStore()
	cfg := store.UpdateConfig(req)
	tenantID, _ := requestScope(r)
	cfg.AccountCount = len(store.ListAccounts(tenantID))
	s.writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleAntigravityQuota(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getAntigravityQuota(w, r)
	case http.MethodPost:
		s.refreshAntigravityQuota(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) getAntigravityQuota(w http.ResponseWriter, r *http.Request) {
	s.writeNotImplemented(w, r, "Antigravity OAuth quota collection is not configured")
}

func (s *Server) refreshAntigravityQuota(w http.ResponseWriter, r *http.Request) {
	s.writeNotImplemented(w, r, "Antigravity OAuth quota collection is not configured")
}

func (s *Server) handleAntigravityQuotaAccount(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	s.writeNotImplemented(w, r, "Antigravity OAuth quota collection is not configured")
}
