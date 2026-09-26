package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ubag/ubag/apps/gateway/internal/antigravity"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

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
	if !antigravityOAuthEnabled() {
		s.writeError(w, r, http.StatusServiceUnavailable, targetError("UBAG-ANTIGRAVITY-OAUTH-001", "Antigravity OAuth CLI is disabled", false, nil))
		return
	}

	var req struct {
		Prompt    string `json:"prompt"`
		Model     string `json:"model,omitempty"`
		AccountID string `json:"account_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

func antigravityOAuthEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_ANTIGRAVITY_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
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
		resp = append(resp, map[string]any{
			"account_id":            a.ID,
			"label":                 a.Label,
			"enabled":               a.Enabled,
			"tier":                  a.Tier,
			"last_used":             a.LastUsed,
			"cooldown_until":        a.CooldownUntil,
			"created_at":            a.CreatedAt,
			"worker_socket_present": workerSocketPresent,
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"accounts": resp})
}

func (s *Server) addAntigravityAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label  string `json:"label"`
		Tier   string `json:"tier"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
			s.writeError(w, r, http.StatusConflict, validationError("UBAG-ANTIGRAVITY-012", err.Error()))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, internalError(err.Error()))
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
		s.writeError(w, r, http.StatusNotFound, validationError("UBAG-ANTIGRAVITY-008", err.Error()))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"removed": accountID})
}

func (s *Server) updateAntigravityAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	var req struct {
		Enabled *bool  `json:"enabled"`
		Label   string `json:"label"`
		Tier    string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		s.writeError(w, r, http.StatusNotFound, validationError("UBAG-ANTIGRAVITY-010", err.Error()))
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
	}{cfg, antigravityOAuthEnabled()})
}

func (s *Server) updateAntigravityConfig(w http.ResponseWriter, r *http.Request) {
	var req antigravity.Config
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
