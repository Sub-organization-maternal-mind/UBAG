package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	"github.com/ubag/ubag/apps/gateway/internal/payloadpolicy"
	"github.com/ubag/ubag/apps/gateway/internal/siem"
)

// WebhookSecretRotation records a single webhook signing-secret rotation. Only
// opaque secret references are stored; plaintext secrets are never persisted.
type WebhookSecretRotation struct {
	ID                string
	TenantID          string
	AppID             string
	WebhookID         string
	ActiveSecretRef   string
	PreviousSecretRef string
	OverlapUntil      time.Time
	CreatedAt         time.Time
}

// WebhookSecretStore persists webhook secret rotations and supports idempotent
// replay (GetByID) and overlap bookkeeping (Latest).
type WebhookSecretStore interface {
	Ready(ctx context.Context) error
	Rotate(ctx context.Context, rotation WebhookSecretRotation) (WebhookSecretRotation, error)
	GetByID(ctx context.Context, id string) (WebhookSecretRotation, bool, error)
	Latest(ctx context.Context, tenantID, appID, webhookID string) (WebhookSecretRotation, bool, error)
}

// MemoryWebhookSecretStore is an in-memory WebhookSecretStore.
type MemoryWebhookSecretStore struct {
	mu       sync.RWMutex
	byID     map[string]WebhookSecretRotation
	latest   map[string]WebhookSecretRotation
	sequence int
}

// NewMemoryWebhookSecretStore returns an empty in-memory store.
func NewMemoryWebhookSecretStore() *MemoryWebhookSecretStore {
	return &MemoryWebhookSecretStore{
		byID:   make(map[string]WebhookSecretRotation),
		latest: make(map[string]WebhookSecretRotation),
	}
}

func (m *MemoryWebhookSecretStore) Ready(context.Context) error { return nil }

func webhookSecretScopeKey(tenantID, appID, webhookID string) string {
	return tenantID + "\x00" + appID + "\x00" + webhookID
}

func (m *MemoryWebhookSecretStore) Rotate(_ context.Context, rotation WebhookSecretRotation) (WebhookSecretRotation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rotation.CreatedAt.IsZero() {
		rotation.CreatedAt = time.Now().UTC()
	}
	m.byID[rotation.ID] = rotation
	m.latest[webhookSecretScopeKey(rotation.TenantID, rotation.AppID, rotation.WebhookID)] = rotation
	return rotation, nil
}

func (m *MemoryWebhookSecretStore) GetByID(_ context.Context, id string) (WebhookSecretRotation, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rotation, ok := m.byID[id]
	return rotation, ok, nil
}

func (m *MemoryWebhookSecretStore) Latest(_ context.Context, tenantID, appID, webhookID string) (WebhookSecretRotation, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rotation, ok := m.latest[webhookSecretScopeKey(tenantID, appID, webhookID)]
	return rotation, ok, nil
}

// SQLiteWebhookSecretStore persists rotations in a SQL database (SQLite or
// Postgres-compatible). Only secret references are stored.
type SQLiteWebhookSecretStore struct {
	db *sql.DB
}

// NewSQLiteWebhookSecretStore builds a SQL-backed store.
func NewSQLiteWebhookSecretStore(db *sql.DB) *SQLiteWebhookSecretStore {
	return &SQLiteWebhookSecretStore{db: db}
}

func (s *SQLiteWebhookSecretStore) Ready(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS webhook_secret_rotations (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    webhook_id TEXT NOT NULL,
    active_secret_ref TEXT NOT NULL,
    previous_secret_ref TEXT NOT NULL DEFAULT '',
    overlap_until TIMESTAMP,
    created_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_webhook_secret_scope
    ON webhook_secret_rotations (tenant_id, app_id, webhook_id, created_at);
`)
	return err
}

func (s *SQLiteWebhookSecretStore) Rotate(ctx context.Context, rotation WebhookSecretRotation) (WebhookSecretRotation, error) {
	if rotation.CreatedAt.IsZero() {
		rotation.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO webhook_secret_rotations
    (id, tenant_id, app_id, webhook_id, active_secret_ref, previous_secret_ref, overlap_until, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rotation.ID, rotation.TenantID, rotation.AppID, rotation.WebhookID,
		rotation.ActiveSecretRef, rotation.PreviousSecretRef, rotation.OverlapUntil.UTC(), rotation.CreatedAt.UTC())
	if err != nil {
		return WebhookSecretRotation{}, err
	}
	return rotation, nil
}

func (s *SQLiteWebhookSecretStore) GetByID(ctx context.Context, id string) (WebhookSecretRotation, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, tenant_id, app_id, webhook_id, active_secret_ref, previous_secret_ref, overlap_until, created_at
FROM webhook_secret_rotations WHERE id = ?`, id)
	rotation, err := scanWebhookSecretRotation(row)
	if err == sql.ErrNoRows {
		return WebhookSecretRotation{}, false, nil
	}
	if err != nil {
		return WebhookSecretRotation{}, false, err
	}
	return rotation, true, nil
}

func (s *SQLiteWebhookSecretStore) Latest(ctx context.Context, tenantID, appID, webhookID string) (WebhookSecretRotation, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, tenant_id, app_id, webhook_id, active_secret_ref, previous_secret_ref, overlap_until, created_at
FROM webhook_secret_rotations
WHERE tenant_id = ? AND app_id = ? AND webhook_id = ?
ORDER BY created_at DESC LIMIT 1`, tenantID, appID, webhookID)
	rotation, err := scanWebhookSecretRotation(row)
	if err == sql.ErrNoRows {
		return WebhookSecretRotation{}, false, nil
	}
	if err != nil {
		return WebhookSecretRotation{}, false, err
	}
	return rotation, true, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWebhookSecretRotation(row rowScanner) (WebhookSecretRotation, error) {
	var rotation WebhookSecretRotation
	var overlap sql.NullTime
	if err := row.Scan(
		&rotation.ID, &rotation.TenantID, &rotation.AppID, &rotation.WebhookID,
		&rotation.ActiveSecretRef, &rotation.PreviousSecretRef, &overlap, &rotation.CreatedAt,
	); err != nil {
		return WebhookSecretRotation{}, err
	}
	if overlap.Valid {
		rotation.OverlapUntil = overlap.Time.UTC()
	}
	rotation.CreatedAt = rotation.CreatedAt.UTC()
	return rotation, nil
}

type webhookSecretRotateRequest struct {
	APIVersion     string `json:"api_version,omitempty"`
	WebhookID      string `json:"webhook_id"`
	NewSecretRef   string `json:"new_secret_ref"`
	OverlapSeconds int    `json:"overlap_seconds,omitempty"`
}

type webhookSecretRotateResponse struct {
	APIVersion        string     `json:"api_version"`
	Status            string     `json:"status"`
	WebhookID         string     `json:"webhook_id"`
	ActiveSecretRef   string     `json:"active_secret_ref"`
	PreviousSecretRef string     `json:"previous_secret_ref,omitempty"`
	OverlapUntil      *time.Time `json:"overlap_until,omitempty"`
	TraceID           string     `json:"trace_id"`
}

func (s *Server) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if s.webhookSecrets == nil {
		s.writeNotImplemented(w, r, "webhook secret rotation is not configured")
		return
	}
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var request webhookSecretRotateRequest
	if !s.decodeBody(w, r, raw, &request) {
		return
	}
	apiVersion, ok := s.resolveAPIVersion(w, r, request.APIVersion)
	if !ok {
		return
	}
	idempotencyKey, ok := s.requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	if !s.authorizeGatewayAction(w, r, "secret:rotate") {
		return
	}

	// new_secret_ref / previous_secret_ref are opaque references (keys ending
	// in _secret_ref are allowed by payloadpolicy); plaintext secrets are still
	// rejected by value-pattern scanning.
	var bodyMap map[string]any
	if err := json.Unmarshal(raw, &bodyMap); err == nil {
		if err := payloadpolicy.Validate(bodyMap); err != nil {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WEBHOOK-SECRET-PAYLOAD-SAFETY-001", err.Error()))
			return
		}
	}
	if strings.TrimSpace(request.WebhookID) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WEBHOOK-SECRET-ID-001", "webhook_id is required"))
		return
	}
	if strings.TrimSpace(request.NewSecretRef) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WEBHOOK-SECRET-REF-001", "new_secret_ref is required"))
		return
	}
	if request.OverlapSeconds < 0 {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WEBHOOK-SECRET-OVERLAP-001", "overlap_seconds must not be negative"))
		return
	}

	tenantID, appID := requestScope(r)
	scope := idempotency.Scope{
		TenantID:  tenantID,
		AppID:     appID,
		Operation: "rotate_webhook_secret",
		Key:       idempotencyKey,
	}
	decision, err := s.idempotency.Reserve(r.Context(), scope, hashBytes(raw))
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to reserve idempotency key"))
		return
	}
	switch decision.Kind {
	case idempotency.DecisionConflict:
		s.writeError(w, r, http.StatusConflict, validationError("UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001", "idempotency key was replayed with a different payload"))
		return
	case idempotency.DecisionReplay:
		prior, found, getErr := s.webhookSecrets.GetByID(r.Context(), decision.Record.ResourceID)
		if getErr != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load prior rotation"))
			return
		}
		if found {
			s.writeJSON(w, replayHTTPStatus(decision.Record, http.StatusOK), s.webhookRotationToResponse(apiVersion, prior, traceIDFromContext(r.Context())))
			return
		}
	}

	previousRef := ""
	if latest, found, latestErr := s.webhookSecrets.Latest(r.Context(), tenantID, appID, strings.TrimSpace(request.WebhookID)); latestErr == nil && found {
		previousRef = latest.ActiveSecretRef
	}

	now := time.Now().UTC()
	rotation := WebhookSecretRotation{
		ID:                "whs_" + hashString(scope.CacheKey()),
		TenantID:          tenantID,
		AppID:             appID,
		WebhookID:         strings.TrimSpace(request.WebhookID),
		ActiveSecretRef:   strings.TrimSpace(request.NewSecretRef),
		PreviousSecretRef: previousRef,
		CreatedAt:         now,
	}
	if request.OverlapSeconds > 0 {
		rotation.OverlapUntil = now.Add(time.Duration(request.OverlapSeconds) * time.Second)
	}

	stored, err := s.webhookSecrets.Rotate(r.Context(), rotation)
	if err != nil {
		_ = s.idempotency.Release(r.Context(), scope)
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to persist webhook secret rotation"))
		return
	}
	if err := s.idempotency.Complete(r.Context(), scope, stored.ID, http.StatusOK); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to complete idempotency record"))
		return
	}

	// Emit an audit event when a SIEM exporter is configured.
	if s.siemExporter != nil {
		s.siemExporter.Enqueue(siem.Redact(siem.Event{
			TenantID:  tenantID,
			AppID:     appID,
			Type:      "audit",
			Action:    "webhook.secret.rotate",
			Actor:     s.actorRole,
			Resource:  stored.WebhookID,
			Outcome:   "success",
			Timestamp: now,
			Attributes: map[string]any{
				"active_secret_ref": stored.ActiveSecretRef,
				"rotation_id":       stored.ID,
			},
		}))
	}

	s.writeJSON(w, http.StatusOK, s.webhookRotationToResponse(apiVersion, stored, traceIDFromContext(r.Context())))
}

func (s *Server) webhookRotationToResponse(apiVersion string, rotation WebhookSecretRotation, traceID string) webhookSecretRotateResponse {
	resp := webhookSecretRotateResponse{
		APIVersion:        apiVersion,
		Status:            "rotated",
		WebhookID:         rotation.WebhookID,
		ActiveSecretRef:   rotation.ActiveSecretRef,
		PreviousSecretRef: rotation.PreviousSecretRef,
		TraceID:           traceID,
	}
	if !rotation.OverlapUntil.IsZero() {
		overlap := rotation.OverlapUntil.UTC()
		resp.OverlapUntil = &overlap
	}
	return resp
}
