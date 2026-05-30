// Package alerts implements the UBAG gateway's human-in-the-loop alerting
// subsystem. When a worker reports that a job needs a manual human action
// (CAPTCHA, manual login, or a verification challenge) the gateway raises an
// alert and notifies a human operator (by email) so they can solve it in the
// live browser session and let the flow resume.
//
// This is the ToS-safe design: humans solve CAPTCHAs and verification
// challenges, never the machine.
//
// Three store backends mirror the gateway's audit/session store conventions:
// an in-memory store (default / tests), a SQLite store, and a Postgres store.
// Notification delivery is pluggable via AlertSink (LogSink, SMTPEmailSink,
// MultiSink).
package alerts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultRecipient is the fallback alert email recipient used when
// UBAG_ALERT_EMAIL_TO is not configured.
const DefaultRecipient = "mindreader420123@gmail.com"

// Alert lifecycle statuses.
const (
	StatusOpen         = "open"
	StatusNotified     = "notified"
	StatusAcknowledged = "acknowledged"
	StatusResolved     = "resolved"
	StatusExpired      = "expired"
)

// Alert kinds describing the manual action a human must perform.
const (
	KindCaptcha      = "captcha"
	KindManualLogin  = "manual_login"
	KindVerification = "verification"
	KindDrift        = "drift"
	KindOther        = "other"
)

// defaultDispatchTimeout bounds how long a single notification dispatch may run
// so a slow SMTP server never stalls ingestion.
const defaultDispatchTimeout = 15 * time.Second

// Alert is a single human-in-the-loop manual-action request.
type Alert struct {
	AlertID    string         `json:"alert_id"`
	TenantID   string         `json:"tenant_id"`
	AppID      string         `json:"app_id"`
	JobID      string         `json:"job_id"`
	SessionID  string         `json:"session_id,omitempty"`
	TargetID   string         `json:"target_id,omitempty"`
	Kind       string         `json:"kind"`
	Message    string         `json:"message,omitempty"`
	Status     string         `json:"status"`
	CreatedAt  time.Time      `json:"created_at"`
	NotifiedAt time.Time      `json:"notified_at,omitempty"`
	AckedAt    time.Time      `json:"acknowledged_at,omitempty"`
	ResolvedAt time.Time      `json:"resolved_at,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Filter constrains a List query.
type Filter struct {
	TenantID string
	Status   string // optional; empty means any status
	Limit    int    // 0 means no limit
}

// Store persists alerts and exposes lifecycle transitions.
type Store interface {
	Ready(ctx context.Context) error
	// Raise inserts a new alert, deduping by (tenant_id, job_id, kind) while an
	// existing alert for that triple is still active (open/notified/
	// acknowledged). It returns the live alert and whether a new row was
	// created (created=false means an active alert already existed).
	Raise(ctx context.Context, alert Alert) (Alert, bool, error)
	// Get returns a single alert scoped to its tenant.
	Get(ctx context.Context, tenantID, alertID string) (Alert, bool, error)
	// UpdateStatus transitions an alert to status, stamping the corresponding
	// timestamp from at.
	UpdateStatus(ctx context.Context, tenantID, alertID, status string, at time.Time) (Alert, bool, error)
	// List returns alerts matching filter ordered by CreatedAt descending
	// (newest first).
	List(ctx context.Context, filter Filter) ([]Alert, error)
}

// AlertSink delivers an alert notification to a human (email, log, etc.).
type AlertSink interface {
	Send(ctx context.Context, alert Alert) error
}

// ConfigSummary is a secret-free description of the alerting configuration for
// the read-only /v1/alerts/config endpoint. It NEVER contains SMTP credentials.
type ConfigSummary struct {
	SinkType       string   `json:"sink_type"`
	SMTPConfigured bool     `json:"smtp_configured"`
	SMTPHost       string   `json:"smtp_host,omitempty"`
	StoreKind      string   `json:"store_kind,omitempty"`
	RecipientCount int      `json:"recipient_count"`
	Recipients     []string `json:"recipients,omitempty"`
}

// ---------------------------------------------------------------------------
// Helpers shared across store backends.
// ---------------------------------------------------------------------------

// validKinds is the allowlist of recognised manual-action kinds. Unknown kinds
// are normalised to KindOther.
var validKinds = map[string]struct{}{
	KindCaptcha:      {},
	KindManualLogin:  {},
	KindVerification: {},
	KindDrift:        {},
	KindOther:        {},
}

func normalizeKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if _, ok := validKinds[kind]; ok {
		return kind
	}
	return KindOther
}

func isActiveStatus(status string) bool {
	switch status {
	case StatusOpen, StatusNotified, StatusAcknowledged:
		return true
	}
	return false
}

// canonicalTime renders t as a microsecond-precision UTC RFC3339 string so it
// round-trips identically through Postgres TIMESTAMPTZ and SQLite TEXT.
func canonicalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z07:00")
}

func parseCanonicalTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse("2006-01-02T15:04:05.000000Z07:00", value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("alerts: parse time %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

func canonicalAttributes(attrs map[string]any) (string, error) {
	if len(attrs) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(attrs)
	if err != nil {
		return "", fmt.Errorf("alerts: marshal attributes: %w", err)
	}
	return string(encoded), nil
}

func decodeAttributes(encoded string) map[string]any {
	if encoded == "" || encoded == "{}" {
		return nil
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(encoded), &attrs); err != nil {
		return nil
	}
	return attrs
}

func stableID(prefix string, parts ...any) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(parts...)))
	return prefix + "_" + hex.EncodeToString(sum[:])[:24]
}

// prepare normalises an incoming alert prior to persistence.
func prepare(alert *Alert) {
	alert.TenantID = strings.TrimSpace(alert.TenantID)
	alert.AppID = strings.TrimSpace(alert.AppID)
	alert.JobID = strings.TrimSpace(alert.JobID)
	alert.SessionID = strings.TrimSpace(alert.SessionID)
	alert.TargetID = strings.TrimSpace(alert.TargetID)
	alert.Message = strings.TrimSpace(alert.Message)
	alert.Kind = normalizeKind(alert.Kind)
	alert.Status = StatusOpen
	if alert.CreatedAt.IsZero() {
		alert.CreatedAt = time.Now()
	}
	alert.CreatedAt = alert.CreatedAt.UTC().Truncate(time.Microsecond)
	alert.NotifiedAt = time.Time{}
	alert.AckedAt = time.Time{}
	alert.ResolvedAt = time.Time{}
	if alert.AlertID == "" {
		alert.AlertID = stableID("alert", alert.TenantID, alert.JobID, alert.Kind, alert.CreatedAt.UnixNano())
	}
}

func applyStatusTimestamp(alert *Alert, status string, at time.Time) {
	at = at.UTC().Truncate(time.Microsecond)
	alert.Status = status
	switch status {
	case StatusNotified:
		alert.NotifiedAt = at
	case StatusAcknowledged:
		alert.AckedAt = at
	case StatusResolved, StatusExpired:
		alert.ResolvedAt = at
	}
}

// ---------------------------------------------------------------------------
// MemoryStore
// ---------------------------------------------------------------------------

// MemoryStore is an in-memory Store, primarily for development and tests.
type MemoryStore struct {
	mu       sync.Mutex
	byTenant map[string][]Alert
}

// NewMemoryStore returns an empty in-memory alert store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byTenant: make(map[string][]Alert)}
}

func (m *MemoryStore) Ready(context.Context) error { return nil }

func (m *MemoryStore) Raise(_ context.Context, alert Alert) (Alert, bool, error) {
	prepare(&alert)
	m.mu.Lock()
	defer m.mu.Unlock()

	chain := m.byTenant[alert.TenantID]
	for _, existing := range chain {
		if existing.JobID == alert.JobID && existing.Kind == alert.Kind && isActiveStatus(existing.Status) {
			return existing, false, nil
		}
	}
	m.byTenant[alert.TenantID] = append(chain, alert)
	return alert, true, nil
}

func (m *MemoryStore) Get(_ context.Context, tenantID, alertID string) (Alert, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.byTenant[tenantID] {
		if existing.AlertID == alertID {
			return existing, true, nil
		}
	}
	return Alert{}, false, nil
}

func (m *MemoryStore) UpdateStatus(_ context.Context, tenantID, alertID, status string, at time.Time) (Alert, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.byTenant[tenantID]
	for i := range chain {
		if chain[i].AlertID == alertID {
			applyStatusTimestamp(&chain[i], status, at)
			return chain[i], true, nil
		}
	}
	return Alert{}, false, nil
}

func (m *MemoryStore) List(_ context.Context, filter Filter) ([]Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.byTenant[filter.TenantID]
	out := make([]Alert, 0, len(chain))
	for _, existing := range chain {
		if filter.Status != "" && existing.Status != filter.Status {
			continue
		}
		out = append(out, existing)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Sinks
// ---------------------------------------------------------------------------

// LogSink records alerts to a structured logger. It is the safe default when no
// email transport is configured.
type LogSink struct {
	logger *slog.Logger
}

// NewLogSink constructs a LogSink. A nil logger falls back to slog.Default.
func NewLogSink(logger *slog.Logger) *LogSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogSink{logger: logger}
}

func (s *LogSink) Send(_ context.Context, alert Alert) error {
	s.logger.Warn("manual action required",
		"alert_id", alert.AlertID,
		"tenant_id", alert.TenantID,
		"job_id", alert.JobID,
		"kind", alert.Kind,
		"session_id", alert.SessionID,
		"target_id", alert.TargetID,
		"message", alert.Message,
	)
	return nil
}

// MultiSink fans an alert out to several sinks, best-effort: every sink is
// attempted and their errors are joined.
type MultiSink struct {
	sinks []AlertSink
}

// NewMultiSink constructs a MultiSink over the supplied sinks (nil sinks are
// ignored).
func NewMultiSink(sinks ...AlertSink) *MultiSink {
	filtered := make([]AlertSink, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			filtered = append(filtered, sink)
		}
	}
	return &MultiSink{sinks: filtered}
}

func (s *MultiSink) Send(ctx context.Context, alert Alert) error {
	var errs []error
	for _, sink := range s.sinks {
		if err := sink.Send(ctx, alert); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager coordinates alert persistence and notification dispatch.
type Manager struct {
	store           Store
	sink            AlertSink
	logger          *slog.Logger
	summary         ConfigSummary
	dispatchTimeout time.Duration
	now             func() time.Time

	// dispatchSync forces synchronous notification dispatch. Production leaves
	// it false so a slow SMTP server never blocks worker ingestion; tests set
	// it true for determinism.
	dispatchSync bool
	wg           sync.WaitGroup
}

// NewManager constructs a Manager. A nil sink disables notification dispatch
// (alerts are still persisted). A nil logger falls back to slog.Default.
func NewManager(store Store, sink AlertSink, logger *slog.Logger, summary ConfigSummary) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		store:           store,
		sink:            sink,
		logger:          logger,
		summary:         summary,
		dispatchTimeout: defaultDispatchTimeout,
		now:             time.Now,
	}
}

// Ready verifies the underlying store is reachable.
func (m *Manager) Ready(ctx context.Context) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("alerts: manager is not configured")
	}
	return m.store.Ready(ctx)
}

// Config returns the secret-free alerting configuration summary.
func (m *Manager) Config() ConfigSummary {
	if m == nil {
		return ConfigSummary{}
	}
	return m.summary
}

// RaiseManualAction persists a manual-action alert and, when it is newly
// created, dispatches a notification. Dedupe keeps a single active alert per
// (tenant, job, kind) so repeated worker events do not spam operators. It is
// nil-safe so ingestion paths can call it unconditionally.
func (m *Manager) RaiseManualAction(ctx context.Context, alert Alert) (Alert, error) {
	if m == nil || m.store == nil {
		return Alert{}, nil
	}
	persisted, created, err := m.store.Raise(ctx, alert)
	if err != nil {
		return Alert{}, err
	}
	if created {
		m.notify(persisted)
	}
	return persisted, nil
}

func (m *Manager) notify(alert Alert) {
	if m.sink == nil {
		return
	}
	if m.dispatchSync {
		m.dispatchOnce(alert)
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.dispatchOnce(alert)
	}()
}

func (m *Manager) dispatchOnce(alert Alert) {
	ctx, cancel := context.WithTimeout(context.Background(), m.dispatchTimeout)
	defer cancel()
	if err := m.sink.Send(ctx, alert); err != nil {
		m.logger.Warn("alert notification failed",
			"alert_id", alert.AlertID,
			"tenant_id", alert.TenantID,
			"job_id", alert.JobID,
			"error", err.Error(),
		)
		return
	}
	if _, _, err := m.store.UpdateStatus(ctx, alert.TenantID, alert.AlertID, StatusNotified, m.now()); err != nil {
		m.logger.Warn("alert notified-state update failed",
			"alert_id", alert.AlertID,
			"error", err.Error(),
		)
	}
}

// Acknowledge marks an alert as being handled by a human.
func (m *Manager) Acknowledge(ctx context.Context, tenantID, alertID string) (Alert, bool, error) {
	if m == nil || m.store == nil {
		return Alert{}, false, fmt.Errorf("alerts: manager is not configured")
	}
	return m.store.UpdateStatus(ctx, tenantID, alertID, StatusAcknowledged, m.now())
}

// Resolve marks an alert as solved so the flow may resume.
func (m *Manager) Resolve(ctx context.Context, tenantID, alertID string) (Alert, bool, error) {
	if m == nil || m.store == nil {
		return Alert{}, false, fmt.Errorf("alerts: manager is not configured")
	}
	return m.store.UpdateStatus(ctx, tenantID, alertID, StatusResolved, m.now())
}

// List returns alerts matching filter.
func (m *Manager) List(ctx context.Context, filter Filter) ([]Alert, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("alerts: manager is not configured")
	}
	return m.store.List(ctx, filter)
}

// Wait blocks until all in-flight asynchronous dispatches complete. It is
// primarily useful in tests and graceful shutdown.
func (m *Manager) Wait() {
	if m == nil {
		return
	}
	m.wg.Wait()
}
