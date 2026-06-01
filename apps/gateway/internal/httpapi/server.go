package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	crypto_rsa "crypto/rsa"
	"os"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"math"

	"github.com/go-chi/chi/v5"
	"github.com/ubag/ubag/apps/gateway/internal/abac"
	"github.com/ubag/ubag/apps/gateway/internal/alerts"
	"github.com/ubag/ubag/apps/gateway/internal/appjwt"
	"github.com/ubag/ubag/apps/gateway/internal/compliance"
	"github.com/ubag/ubag/apps/gateway/internal/jitadmin"
	"github.com/ubag/ubag/apps/gateway/internal/mfa"
	"github.com/ubag/ubag/apps/gateway/internal/outbox"
	"github.com/ubag/ubag/apps/gateway/internal/pat"
	"github.com/ubag/ubag/apps/gateway/internal/plugins"
	"github.com/ubag/ubag/apps/gateway/internal/resilience"
	"github.com/ubag/ubag/apps/gateway/internal/semanticcache"
	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	"github.com/ubag/ubag/apps/gateway/internal/jobcore"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	mw "github.com/ubag/ubag/apps/gateway/internal/middleware"
	"github.com/ubag/ubag/apps/gateway/internal/ratelimit"
	"github.com/ubag/ubag/apps/gateway/internal/region"
	"github.com/ubag/ubag/apps/gateway/internal/responsecache"
	"github.com/ubag/ubag/apps/gateway/internal/scim"
	"github.com/ubag/ubag/apps/gateway/internal/session"
	"github.com/ubag/ubag/apps/gateway/internal/siem"
	"github.com/ubag/ubag/apps/gateway/internal/sso"
	"github.com/ubag/ubag/apps/gateway/internal/templates"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	"github.com/ubag/ubag/apps/gateway/internal/webhooks"
	"github.com/ubag/ubag/apps/gateway/internal/workflow"
)

const (
	defaultMaxBodyBytes = 1 << 20
	defaultTenantID     = "tenant_edge"
	defaultAppID        = "app_default"

	headerAPIVersion     = "Ubag-Api-Version"
	headerIdempotencyKey = "Idempotency-Key"
	headerTenantID       = "Ubag-Tenant-Id"
	headerAppID          = "Ubag-App-Id"
)

var (
	apiVersionPattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{16,128}$`)
	targetPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type Config struct {
	APIVersion  string
	Version     string
	BuildCommit string
	AppSecret   string
	TenantID    string
	AppID       string
	ActorRole   string

	Jobs        jobstore.Store
	Idempotency idempotency.Service
	Executor    executor.Dispatcher
	Artifacts   artifacts.ArtifactStore
	Templates   templates.Store
	Webhooks    webhooks.OutboxStore

	WebhookURLPolicy webhooks.URLPolicy

	// Optional enterprise components. Every field below is nil-safe: when a
	// component is nil the corresponding route returns a clean 501/empty
	// result (or, for rate limiting, the middleware becomes a pass-through).
	// NewServer must continue to work when all of these are zero/nil.

	// RateLimiter + RateLimitResolver enable per-action rate limiting. Both
	// must be non-nil AND RateLimitEnabled true for the middleware to enforce.
	RateLimiter       ratelimit.Limiter
	RateLimitResolver *ratelimit.PolicyResolver
	RateLimitEnabled  bool

	// ResponseCache backs GET/DELETE /v1/cache. Nil leaves the legacy
	// disabled-cache status response intact.
	ResponseCache *responsecache.Cache

	// Workflows + WorkflowEngine back the /v1/workflows routes. When Workflows
	// is set but WorkflowEngine is nil, NewServer constructs a default engine.
	Workflows      workflow.Store
	WorkflowEngine *workflow.Engine

	// SSO backs the /v1/sso/* routes.
	SSO sso.ConfigStore

	// SSOAuthFlow, when non-nil, enables the OIDC authorization-code flow
	// (GET /v1/sso/oidc/authorize + GET /v1/sso/oidc/callback?code=&state=).
	// When nil only the direct id_token verification flow (POST callback) is available.
	SSOAuthFlow *sso.AuthCodeFlow

	// SCIM backs the /v1/scim/v2/* routes.
	SCIM scim.Store

	// SIEMConfig backs GET/PUT /v1/siem/config; SIEMExporter backs
	// POST /v1/audit/export and webhook-secret-rotation audit emission.
	SIEMConfig   siem.ConfigStore
	SIEMExporter *siem.Exporter

	// WebhookSecrets persists webhook secret rotations. When nil, NewServer
	// installs an in-memory store so the rotation route remains functional.
	WebhookSecrets WebhookSecretStore

	// Audit persists Merkle-chained audit records emitted on authorization
	// decisions and SSO session events, and backs POST /v1/audit/export. When
	// nil, NewServer installs an in-memory store.
	Audit audit.Store

	// Sessions backs server-side SSO sessions (minting on login, resolution in
	// withAuth, revocation on logout). When nil, NewServer installs an in-memory
	// store. SessionTTL is the lifetime of a minted session (default 1h).
	Sessions   session.Store
	SessionTTL time.Duration

	// Alerts backs the human-in-the-loop manual-action alerting subsystem and
	// the /v1/alerts* routes. Unlike the stores above it is NOT defaulted by
	// NewServer: when nil the alert routes return 501 so an unconfigured
	// deployment degrades cleanly.
	Alerts *alerts.Manager

	// Topology backs the read-only v2.1 multi-tab browser topology routes
	// (/v1/browser/*). Like Alerts it is NOT defaulted by NewServer: when nil the
	// routes return 501 so an unconfigured deployment degrades cleanly.
	Topology topology.Store

	// Concurrency backs the read-only adaptive-concurrency (AIMD) view route
	// (/v1/concurrency). When nil the route returns 501. The registry is updated
	// by the worker-event ingestion path; the gateway never mutates it via HTTP.
	Concurrency *topology.ConcurrencyRegistry

	// Outbox, when non-nil, receives job-dispatch events atomically with job
	// creation. The relay delivers them to NATS independently. When nil,
	// EnqueueJob is called directly (backward-compatible).
	Outbox outbox.Store

	// PAT, when non-nil, enables Personal Access Token issuance and validation
	// (§11). POST /v1/auth/pat issues tokens; Bearer ubag_pat_... authenticates.
	PAT pat.Store

	// PATDefaultTTL is the default TTL for issued PATs. Zero means no expiry.
	PATDefaultTTL time.Duration

	// AppJWTPublicKey, when non-nil, enables App JWT authentication (§11).
	// Requests bearing a Bearer RS256 JWT signed with the matching private key
	// are accepted. The JWT must carry tid (tenant_id), sub (app_id), and role.
	AppJWTPublicKey *crypto_rsa.PublicKey

	// ABACEnforcer, when non-nil, applies CEL policy rules after the RBAC role
	// check in authorizeGatewayAction. A nil enforcer is permissive (RBAC only).
	ABACEnforcer *abac.Enforcer

	// SemanticCache provides the §17 semantic response cache (SHA-256 exact +
	// pgvector cosine similarity). When nil, the legacy ResponseCache remains
	// active. Both may be configured simultaneously.
	SemanticCache semanticcache.Store

	// PrivacyStore backs POST /v1/privacy/export and /v1/privacy/erase (§28).
	// When nil, the routes return 501.
	PrivacyStore compliance.Store

	// MaxQueueDepth is the pending-job ceiling before the gateway returns
	// UBAG-QUEUE-BACKPRESSURE-002 (429). Zero disables the check.
	// Defaults to UBAG_MAX_QUEUE_DEPTH env var if set, or 10000.
	MaxQueueDepth int

	MaxBodyBytes int64

	// Plugins is the optional WASM plugin host. When nil, no plugin hooks run.
	Plugins *plugins.Host

	// RegionRouter enables region-aware job routing (enterprise, GeoReplication=On).
	// When nil the gateway runs in single-region mode (subject uses "default" segment).
	RegionRouter *region.Router

	// KillSwitch enables per-region operational state management (enterprise).
	// When nil the gateway acts as if the current region is always active.
	KillSwitch *region.KillSwitch

	// MFA, when non-nil, enables TOTP-based multi-factor authentication (§MFA).
	// POST /v1/mfa/enroll and POST /v1/mfa/verify are active only when this is set.
	// When nil, both routes return 501.
	MFA *mfa.Service

	// JITAdmin enables time-boxed privilege elevation (enterprise).
	// When nil, the elevation routes return 501.
	JITAdmin jitadmin.Store
}

type Server struct {
	apiVersion  string
	version     string
	buildCommit string
	appSecret   string
	tenantID    string
	appID       string
	actorRole   string
	maxBody     int64
	jobs        jobstore.Store
	idempotency idempotency.Service
	executor    executor.Dispatcher
	artifactSt  artifacts.ArtifactStore
	templates   templates.Store
	webhooks    webhooks.OutboxStore
	webhookURLs webhooks.URLPolicy

	rateLimiter      ratelimit.Limiter
	rateResolver     *ratelimit.PolicyResolver
	rateLimitEnabled bool
	responseCache    *responsecache.Cache
	workflows        workflow.Store
	workflowEngine   *workflow.Engine
	sso              sso.ConfigStore
	ssoAuthFlow      *sso.AuthCodeFlow
	scim             scim.Store
	siemConfig       siem.ConfigStore
	siemExporter     *siem.Exporter
	webhookSecrets   WebhookSecretStore
	audit            audit.Store
	sessions         session.Store
	sessionTTL       time.Duration
	alerts           *alerts.Manager
	topology         topology.Store
	concurrency      *topology.ConcurrencyRegistry
	outbox           outbox.Store
	maxQueueDepth    int
	patStore         pat.Store
	patDefaultTTL    time.Duration
	appJWTPublicKey  *crypto_rsa.PublicKey
	abacEnforcer     *abac.Enforcer
	semanticCache    semanticcache.Store
	privacyStore     compliance.Store
	plugins          *plugins.Host // nil-safe: no host → no hooks run
	regionRouter     *region.Router
	killSwitch       *region.KillSwitch
	mfaSvc           *mfa.Service
	mfaSessions      mfaSessionSet // in-memory set of MFA-verified session IDs
	jitAdmin         jitadmin.Store

	// §18 contract counters (Task 2.3) — updated atomically on the hot path.
	idempotencyReplays atomic.Int64 // ubag_idempotency_replays_total
	artifactCaptures   atomic.Int64 // ubag_artifact_captures_total
	webhookDeliveries  atomic.Int64 // ubag_webhook_deliveries_total

	metrics *metricState
	mux     chi.Router
}

type metricState struct {
	mu          sync.Mutex
	requests    map[string]int
	durationSum map[string]float64
	sseCurrent  int
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(body []byte) (int, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return recorder.ResponseWriter.Write(body)
}

func (recorder *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := recorder.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not implement http.Hijacker")
	}
	recorder.status = http.StatusSwitchingProtocols
	return hijacker.Hijack()
}

func NewServer(config Config) *Server {
	if config.APIVersion == "" {
		config.APIVersion = DefaultAPIVersion
	}
	if config.Version == "" {
		config.Version = "0.0.0-dev"
	}
	if config.BuildCommit == "" {
		config.BuildCommit = "unknown"
	}
	if config.AppSecret == "" {
		config.AppSecret = generatedTraceID()
	}
	if strings.TrimSpace(config.TenantID) == "" {
		config.TenantID = defaultTenantID
	}
	if strings.TrimSpace(config.AppID) == "" {
		config.AppID = defaultAppID
	}
	if strings.TrimSpace(config.ActorRole) == "" {
		config.ActorRole = "service"
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = defaultMaxBodyBytes
	}
	if config.MaxQueueDepth <= 0 {
		config.MaxQueueDepth = parseEnvInt("UBAG_MAX_QUEUE_DEPTH", 10000)
	}
	if config.Jobs == nil {
		config.Jobs = jobstore.NewMemoryStore()
	}
	if config.Idempotency == nil {
		config.Idempotency = idempotency.NewMemoryStore(24 * time.Hour)
	}
	if config.Executor == nil {
		config.Executor = executor.NewNoopDispatcher()
	}
	if config.Artifacts == nil {
		config.Artifacts = artifacts.NewMemoryArtifactStore()
	}
	if config.Templates == nil {
		config.Templates = templates.NewMemoryStore()
	}
	if config.Webhooks == nil {
		config.Webhooks = webhooks.NewMemoryStore()
	}
	if config.Workflows != nil && config.WorkflowEngine == nil {
		config.WorkflowEngine = workflow.NewEngine(config.Workflows)
	}
	if config.WebhookSecrets == nil {
		config.WebhookSecrets = NewMemoryWebhookSecretStore()
	}
	if config.Audit == nil {
		config.Audit = audit.NewMemoryStore()
	}
	if config.Sessions == nil {
		config.Sessions = session.NewMemoryStore()
	}
	if config.SessionTTL <= 0 {
		config.SessionTTL = time.Hour
	}

	server := &Server{
		apiVersion:  config.APIVersion,
		version:     config.Version,
		buildCommit: config.BuildCommit,
		appSecret:   config.AppSecret,
		tenantID:    strings.TrimSpace(config.TenantID),
		appID:       strings.TrimSpace(config.AppID),
		actorRole:   strings.TrimSpace(config.ActorRole),
		maxBody:     config.MaxBodyBytes,
		jobs:        config.Jobs,
		idempotency: config.Idempotency,
		executor:    config.Executor,
		artifactSt:  config.Artifacts,
		templates:   config.Templates,
		webhooks:    config.Webhooks,
		webhookURLs: config.WebhookURLPolicy,

		rateLimiter:      config.RateLimiter,
		rateResolver:     config.RateLimitResolver,
		rateLimitEnabled: config.RateLimitEnabled,
		responseCache:    config.ResponseCache,
		workflows:        config.Workflows,
		workflowEngine:   config.WorkflowEngine,
		sso:              config.SSO,
		ssoAuthFlow:      config.SSOAuthFlow,
		scim:             config.SCIM,
		siemConfig:       config.SIEMConfig,
		siemExporter:     config.SIEMExporter,
		webhookSecrets:   config.WebhookSecrets,
		audit:            config.Audit,
		sessions:         config.Sessions,
		sessionTTL:       config.SessionTTL,
		alerts:           config.Alerts,
		topology:         config.Topology,
		concurrency:      config.Concurrency,
		outbox:           config.Outbox,
		maxQueueDepth:    config.MaxQueueDepth,
		patStore:         config.PAT,
		patDefaultTTL:    config.PATDefaultTTL,
		appJWTPublicKey:  config.AppJWTPublicKey,
		abacEnforcer:     config.ABACEnforcer,
		semanticCache:    config.SemanticCache,
		privacyStore:     config.PrivacyStore,
		plugins:          config.Plugins,
		regionRouter:     config.RegionRouter,
		killSwitch:       config.KillSwitch,
		mfaSvc:           config.MFA,
		jitAdmin:         config.JITAdmin,

		metrics: &metricState{
			requests:    make(map[string]int),
			durationSum: make(map[string]float64),
		},
		mux: chi.NewRouter(),
	}
	server.routes()

	return server
}

func (s *Server) Handler() http.Handler {
	// Blueprint §7.2 middleware chain is applied in routes() via s.mux.Use() so
	// that it is set up once at construction time and only once. Handler() just
	// returns the fully configured chi router.
	return s.mux
}

func (s *Server) routes() {
	// Blueprint §7.2 middleware chain: trace → recover → log → auth → rate-limit → handle.
	// Registered via chi.Use() so the chain is applied exactly once, at construction.
	s.mux.Use(
		s.withMetrics,              // outermost: always records request timing
		s.withRecovery,             // catches panics before they propagate
		mw.Trace,                   // injects/extracts W3C trace ID (§18.3)
		mw.RequestLog(serviceName), // structured JSON request log line (§18.1)
		s.withAuth,                 // authenticates bearer / device / SSO session
		s.withRateLimit,            // IETF token-bucket rate-limiting (§10.6)
		mw.APIVersionHeader(s.apiVersion), // sets Ubag-Api-Version-Used (§6.5)
	)

	// Custom not-found handler — chi's default writes plain text; we need JSON.
	s.mux.NotFound(s.handleNotFound)
	// Custom method-not-allowed handler.
	s.mux.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		s.writeError(w, r, http.StatusMethodNotAllowed, validationError("UBAG-VALIDATION-METHOD-001", "method not allowed"))
	})

	s.mux.HandleFunc("/v1/health", s.handleHealth)
	s.mux.HandleFunc("/v1/ready", s.handleReady)
	s.mux.HandleFunc("/v1/version", s.handleVersion)
	s.mux.HandleFunc("/v1/metrics", s.handleMetrics)
	s.mux.HandleFunc("/v1/events", s.handleEvents)
	s.mux.HandleFunc("/v1/stream", s.handleStream)
	s.mux.HandleFunc("/v1/workflows", s.handleWorkflows)
	s.mux.HandleFunc("/v1/workflows/*", s.handleWorkflowsSubtree)
	s.mux.HandleFunc("/v1/templates", s.handleTemplates)
	s.mux.HandleFunc("/v1/templates/*", s.handleTemplateRender)
	s.mux.HandleFunc("/v1/targets", s.handleCollection("targets", targetCatalog(), "job:read"))
	s.mux.HandleFunc("/v1/adapters", s.handleCollection("adapters", adapterCatalog(), "job:read"))
	s.mux.HandleFunc("/v1/apps", s.handleCollection("apps", nil, "job:read"))
	s.mux.HandleFunc("/v1/devices", s.handleCollection("devices", nil, "job:read"))
	s.mux.HandleFunc("/v1/webhooks", s.handleCollection("webhooks", nil, "job:read"))
	s.mux.HandleFunc("/v1/webhooks/replay", s.replayWebhook)
	s.mux.HandleFunc("/v1/webhooks/secret:rotate", s.rotateWebhookSecret)
	s.mux.HandleFunc("/v1/cache", s.handleCache)
	s.mux.HandleFunc("/v1/cache/invalidate", s.handleCacheInvalidate)
	s.mux.HandleFunc("/v1/rate-limits", s.handleRateLimits)
	s.mux.HandleFunc("/v1/audit", s.handleCollection("audit", nil, "audit:read"))
	s.mux.HandleFunc("/v1/audit/export", s.handleAuditExport)
	s.mux.HandleFunc("/v1/sso/config", s.handleSSOConfig)
	s.mux.HandleFunc("/v1/sso/oidc/authorize", s.handleSSOOIDCAuthorize)
	s.mux.HandleFunc("/v1/sso/oidc/callback", s.handleSSOOIDCCallback)
	s.mux.HandleFunc("/v1/sso/saml/acs", s.handleSSOSAMLACS)
	s.mux.HandleFunc("/v1/sso/logout", s.handleSSOLogout)
	s.mux.HandleFunc("/v1/scim/v2/Users", s.handleSCIMUsers)
	s.mux.HandleFunc("/v1/scim/v2/Users/*", s.handleSCIMUserByID)
	s.mux.HandleFunc("/v1/scim/v2/Groups", s.handleSCIMGroups)
	s.mux.HandleFunc("/v1/scim/v2/Groups/*", s.handleSCIMGroupByID)
	s.mux.HandleFunc("/v1/siem/config", s.handleSIEMConfig)
	s.mux.HandleFunc("/v1/alerts", s.handleAlerts)
	s.mux.HandleFunc("/v1/alerts/config", s.handleAlertsConfig)
	s.mux.HandleFunc("/v1/alerts/*", s.handleAlertsSubtree)
	s.mux.HandleFunc("/v1/browser/instances", s.handleBrowserInstances)
	s.mux.HandleFunc("/v1/browser/contexts", s.handleBrowserContexts)
	s.mux.HandleFunc("/v1/browser/tabs", s.handleBrowserTabs)
	s.mux.HandleFunc("/v1/browser/summary", s.handleBrowserSummary)
	s.mux.HandleFunc("/v1/concurrency", s.handleConcurrency)
	s.mux.HandleFunc("/v1/jobs", s.handleJobs)
	s.mux.HandleFunc("/v1/jobs/batch", s.handleBatchJobs) // §10, §19.2: up to 100 jobs/request; chi resolves before wildcard
	s.mux.HandleFunc("/v1/jobs/*", s.handleJobByID)       // chi wildcard: all /v1/jobs/{id}/... sub-paths
	s.mux.HandleFunc("/v1/sse/jobs/*", s.handleJobSSE)
	s.mux.HandleFunc("/v1/auth/pat", s.handleIssuePAT)
	s.mux.HandleFunc("/v1/privacy/export", s.handlePrivacyExport)
	s.mux.HandleFunc("/v1/privacy/erase", s.handlePrivacyErase)
	s.mux.HandleFunc("/v1/admin/regions/{region}/state", s.handleSetRegionState)
	s.mux.HandleFunc("/v1/mfa/enroll", s.handleMFAEnroll)
	s.mux.HandleFunc("/v1/mfa/verify", s.handleMFAVerify)
	s.mux.HandleFunc("/v1/admin/elevation", s.handleRequestElevation)
	s.mux.HandleFunc("/v1/admin/elevation/{id}/approve", s.handleApproveElevation)
	// Note: catch-all 404 is handled via s.mux.NotFound() registered above.
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}

	s.writeJSON(w, http.StatusOK, healthResponse{
		Service:   serviceName,
		Status:    "ok",
		Version:   s.version,
		CheckedAt: time.Now().UTC(),
		Checks: map[string]any{
			"process": "ok",
		},
		TraceID: traceIDFromContext(r.Context()),
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}

	if err := s.jobs.Ready(r.Context()); err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-READY-001", "job store is not ready", true))
		return
	}
	if err := s.idempotency.Ready(r.Context()); err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-IDEMPOTENCY-READY-001", "idempotency store is not ready", true))
		return
	}
	if err := s.executor.Ready(r.Context()); err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-EXECUTOR-READY-001", "job executor is not ready", true))
		return
	}
	if err := s.artifactSt.Ready(r.Context()); err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-ARTIFACT-READY-001", "artifact store is not ready", true))
		return
	}
	if err := s.templates.Ready(r.Context()); err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-TEMPLATE-READY-001", "template catalog is not ready", true))
		return
	}
	if err := s.webhooks.Ready(r.Context()); err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-WEBHOOK-READY-001", "webhook outbox is not ready", true))
		return
	}
	if s.alerts != nil {
		if err := s.alerts.Ready(r.Context()); err != nil {
			s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-ALERTS-READY-001", "alert store is not ready", true))
			return
		}
	}
	if s.topology != nil {
		if err := s.topology.Ready(r.Context()); err != nil {
			s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-BROWSER-TOPOLOGY-READY-001", "browser topology store is not ready", true))
			return
		}
	}
	if s.killSwitch != nil && !s.killSwitch.IsReady(r.Context()) {
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-REGION-DISABLED-001", "region is disabled", false))
		return
	}

	s.writeJSON(w, http.StatusOK, healthResponse{
		Service:   serviceName,
		Status:    "ready",
		Version:   s.version,
		CheckedAt: time.Now().UTC(),
		Ready:     true,
		Checks: map[string]any{
			"jobs":        true,
			"idempotency": true,
			"queue":       true,
			"executor":    true,
			"artifacts":   true,
			"templates":   true,
			"webhooks":    true,
		},
		TraceID: traceIDFromContext(r.Context()),
	})
}

// handleSetRegionState implements POST /v1/admin/regions/{region}/state.
// It requires the "region:manage" RBAC action (admin or superadmin).
// Body: {"state": "active"|"draining"|"disabled"}
func (s *Server) handleSetRegionState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if !s.authorizeGatewayAction(w, r, "region:manage") {
		return
	}
	if s.killSwitch == nil {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-REGION-KILLSWITCH-DISABLED-001", "region kill switch is not enabled"))
		return
	}
	regionName := chi.URLParam(r, "region")

	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var body struct {
		State string `json:"state"`
	}
	if !s.decodeBody(w, r, raw, &body) {
		return
	}

	tenantID, appID := requestScope(r)
	principal, _ := principalFromContext(r.Context())
	actor := ""
	if principal.Subject != "" {
		actor = principal.Subject
	}

	newState := region.State(body.State)
	if err := s.killSwitch.SetState(r.Context(), region.Region(regionName), newState, actor, tenantID, appID); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-REGION-STATE-INVALID-001", err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}

	s.writeJSON(w, http.StatusOK, versionResponse{
		Service:           serviceName,
		Version:           s.version,
		APIVersions:       []string{s.apiVersion},
		DefaultAPIVersion: s.apiVersion,
		Commit:            s.buildCommit,
		BuiltAt:           time.Now().UTC(),
		TraceID:           traceIDFromContext(r.Context()),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}

	stateCounts, totalJobs, err := s.jobMetricCounts(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to collect gateway metrics"))
		return
	}
	queueStats, err := s.executor.Stats(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to collect executor metrics"))
		return
	}
	if queueStats.QueueName == "" {
		queueStats.QueueName = "jobs"
	}
	webhookStats, err := s.webhooks.Stats(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to collect webhook metrics"))
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "ubag_gateway_info{version=\"%s\",api_version=\"%s\",commit=\"%s\"} 1\n", promLabel(s.version), promLabel(s.apiVersion), promLabel(s.buildCommit))
	_, _ = fmt.Fprint(w, "ubag_gateway_ready{service=\"ubag-gateway\",check=\"jobs\"} 1\n")
	_, _ = fmt.Fprint(w, "ubag_gateway_ready{service=\"ubag-gateway\",check=\"idempotency\"} 1\n")
	_, _ = fmt.Fprint(w, "ubag_gateway_ready{service=\"ubag-gateway\",check=\"queue\"} 1\n")
	_, _ = fmt.Fprint(w, "ubag_gateway_ready{service=\"ubag-gateway\",check=\"executor\"} 1\n")
	_, _ = fmt.Fprint(w, "ubag_gateway_ready{service=\"ubag-gateway\",check=\"artifacts\"} 1\n")
	_, _ = fmt.Fprint(w, "ubag_gateway_ready{service=\"ubag-gateway\",check=\"webhooks\"} 1\n")
	for _, metric := range s.metricsSnapshot() {
		_, _ = fmt.Fprintf(w, "ubag_gateway_http_requests_total{service=\"ubag-gateway\",route=\"%s\",method=\"%s\",status_class=\"%s\",outcome=\"%s\"} %d\n", promLabel(metric.route), promLabel(metric.method), promLabel(metric.statusClass), promLabel(metric.outcome), metric.count)
		_, _ = fmt.Fprintf(w, "ubag_gateway_http_request_duration_seconds_sum{service=\"ubag-gateway\",route=\"%s\",method=\"%s\",status_class=\"%s\"} %.6f\n", promLabel(metric.route), promLabel(metric.method), promLabel(metric.statusClass), metric.durationSum)
		_, _ = fmt.Fprintf(w, "ubag_gateway_http_request_duration_seconds_count{service=\"ubag-gateway\",route=\"%s\",method=\"%s\",status_class=\"%s\"} %d\n", promLabel(metric.route), promLabel(metric.method), promLabel(metric.statusClass), metric.count)
	}
	_, _ = fmt.Fprint(w, "ubag_gateway_http_inflight_requests{service=\"ubag-gateway\",route=\"all\",method=\"all\"} 0\n")
	_, _ = fmt.Fprintf(w, "ubag_jobs_created_total{target_family=\"all\",command_type=\"all\",source=\"gateway\",outcome=\"accepted\"} %d\n", totalJobs)
	for _, status := range jobstore.LifecycleStatuses() {
		state := string(status)
		_, _ = fmt.Fprintf(w, "ubag_jobs_current{target_family=\"all\",state=\"%s\"} %d\n", state, stateCounts[state])
	}
	for _, state := range queueMetricStates(queueStats) {
		_, _ = fmt.Fprintf(w, "ubag_queue_depth{queue=\"%s\",state=\"%s\"} %d\n", promLabel(queueStats.QueueName), promLabel(state), queueStats.DepthByState[state])
		_, _ = fmt.Fprintf(w, "ubag_queue_oldest_job_age_seconds{queue=\"%s\",state=\"%s\"} %.6f\n", promLabel(queueStats.QueueName), promLabel(state), queueStats.OldestAgeByState[state].Seconds())
	}
	workerSuccess := stateCounts[string(jobstore.StatusCompleted)] + stateCounts[string(jobstore.StatusCompletedWithWarnings)]
	workerFailure := stateCounts[string(jobstore.StatusFailedRetryable)] + stateCounts[string(jobstore.StatusFailedTerminal)] + stateCounts[string(jobstore.StatusDeadLetter)] + stateCounts[string(jobstore.StatusTimedOut)]
	_, _ = fmt.Fprintf(w, "ubag_worker_jobs_processed_total{worker_pool=\"local\",adapter_family=\"mock\",outcome=\"success\"} %d\n", workerSuccess)
	_, _ = fmt.Fprintf(w, "ubag_worker_jobs_processed_total{worker_pool=\"local\",adapter_family=\"mock\",outcome=\"failure\"} %d\n", workerFailure)
	_, _ = fmt.Fprintf(w, "ubag_worker_job_duration_seconds_count{worker_pool=\"local\",adapter_family=\"mock\",outcome=\"success\"} %d\n", workerSuccess)
	_, _ = fmt.Fprint(w, "ubag_worker_job_duration_seconds_sum{worker_pool=\"local\",adapter_family=\"mock\",outcome=\"success\"} 0\n")
	_, _ = fmt.Fprintf(w, "ubag_worker_result_ingestions_total{worker_pool=\"local\",adapter_family=\"mock\",outcome=\"success\",error_class=\"none\"} %d\n", workerSuccess)
	_, _ = fmt.Fprintf(w, "ubag_worker_result_ingestions_total{worker_pool=\"local\",adapter_family=\"mock\",outcome=\"failure\",error_class=\"worker_execution\"} %d\n", workerFailure)
	_, _ = fmt.Fprintf(w, "ubag_worker_result_ingestion_duration_seconds_count{worker_pool=\"local\",adapter_family=\"mock\",outcome=\"success\"} %d\n", workerSuccess)
	_, _ = fmt.Fprint(w, "ubag_worker_result_ingestion_duration_seconds_sum{worker_pool=\"local\",adapter_family=\"mock\",outcome=\"success\"} 0\n")
	for _, state := range webhookMetricStates(webhookStats) {
		_, _ = fmt.Fprintf(w, "ubag_webhook_outbox_depth{endpoint_kind=\"job_callback\",state=\"%s\"} %d\n", promLabel(state), webhookStats.DepthByState[state])
		_, _ = fmt.Fprintf(w, "ubag_webhook_outbox_oldest_age_seconds{endpoint_kind=\"job_callback\",state=\"%s\"} %.6f\n", promLabel(state), webhookStats.OldestAgeByState[state].Seconds())
	}
	_, _ = fmt.Fprintf(w, "ubag_sse_connections_current{service=\"ubag-gateway\"} %d\n", s.currentSSEConnections())

	// §18 contract metrics — missing from original handler (Task 2.3).
	// Idempotency replay counter (incremented in the idempotency replay path).
	idempotencyReplays := s.idempotencyReplays.Load()
	_, _ = fmt.Fprintf(w, "ubag_idempotency_replays_total{service=\"ubag-gateway\",outcome=\"replayed\"} %d\n", idempotencyReplays)

	// Artifact capture counter (incremented by putJobArtifact on success).
	artifactCaptures := s.artifactCaptures.Load()
	_, _ = fmt.Fprintf(w, "ubag_artifact_captures_total{artifact_type=\"file\",outcome=\"success\"} %d\n", artifactCaptures)

	// Adapter-request counters and duration histogram stubs.
	// These are set to 0 in the gateway; real values come from the worker.
	_, _ = fmt.Fprint(w, "ubag_adapter_requests_total{adapter_family=\"mock\",target_family=\"all\",outcome=\"success\",error_class=\"none\"} 0\n")
	_, _ = fmt.Fprint(w, "ubag_adapter_request_duration_seconds_count{adapter_family=\"mock\",target_family=\"all\",outcome=\"success\"} 0\n")
	_, _ = fmt.Fprint(w, "ubag_adapter_request_duration_seconds_sum{adapter_family=\"mock\",target_family=\"all\",outcome=\"success\"} 0\n")

	// Webhook delivery counter and duration histogram.
	webhookDeliveries := s.webhookDeliveries.Load()
	_, _ = fmt.Fprintf(w, "ubag_webhook_deliveries_total{endpoint_kind=\"job_callback\",outcome=\"success\",error_class=\"none\"} %d\n", webhookDeliveries)
	_, _ = fmt.Fprintf(w, "ubag_webhook_delivery_duration_seconds_count{endpoint_kind=\"job_callback\",outcome=\"success\"} %d\n", webhookDeliveries)
	_, _ = fmt.Fprint(w, "ubag_webhook_delivery_duration_seconds_sum{endpoint_kind=\"job_callback\",outcome=\"success\"} 0\n")

	// Job end-to-end duration histogram stub.
	_, _ = fmt.Fprint(w, "ubag_jobs_duration_seconds_count{target_family=\"all\",command_type=\"all\",terminal_state=\"completed\"} 0\n")
	_, _ = fmt.Fprint(w, "ubag_jobs_duration_seconds_sum{target_family=\"all\",command_type=\"all\",terminal_state=\"completed\"} 0\n")
}

func (s *Server) jobMetricCounts(ctx context.Context) (map[string]int, int, error) {
	if metricsStore, ok := s.jobs.(jobstore.MetricsStore); ok {
		counts, total, err := metricsStore.CountsByStatus(ctx, jobstore.ListFilter{})
		if err != nil {
			return nil, 0, err
		}
		result := map[string]int{}
		for status, count := range counts {
			result[string(status)] = count
		}
		return result, total, nil
	}
	jobs, err := s.jobs.List(ctx, jobstore.ListFilter{})
	if err != nil {
		return nil, 0, err
	}
	result := map[string]int{}
	for _, job := range jobs {
		result[string(job.Status)]++
	}
	return result, len(jobs), nil
}

func (s *Server) handleCollection(kind string, data []map[string]any, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			s.writeMethodNotAllowed(w, r, http.MethodGet)
			return
		}
		if action != "" && !s.authorizeGatewayAction(w, r, action) {
			return
		}

		if data == nil {
			data = []map[string]any{}
		}
		limit, ok := s.parseLimit(w, r, r.URL.Query().Get("limit"), 100)
		if !ok {
			return
		}
		page := collectionAfterCursor(data, strings.TrimSpace(r.URL.Query().Get("cursor")))
		nextCursor := collectionNextCursor(page, limit)
		if len(page) > limit {
			page = page[:limit]
		}
		s.writeJSON(w, http.StatusOK, collectionResponse{
			APIVersion: s.apiVersion,
			Kind:       kind,
			Data:       page,
			NextCursor: nextCursor,
			TraceID:    traceIDFromContext(r.Context()),
		})
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	limit, ok := s.parseLimit(w, r, r.URL.Query().Get("limit"), 100)
	if !ok {
		return
	}
	lister, ok := s.jobs.(jobstore.EventLister)
	if !ok {
		s.writeError(w, r, http.StatusInternalServerError, internalError("job event listing is not supported by the configured store"))
		return
	}
	tenantID, appID := requestScope(r)
	events, err := lister.ListAllEvents(r.Context(), jobstore.EventListFilter{
		TenantID:     tenantID,
		AppID:        appID,
		AfterEventID: strings.TrimSpace(r.URL.Query().Get("cursor")),
		Limit:        limit + 1,
	})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list job events"))
		return
	}
	var nextCursor *string
	if len(events) > limit {
		cursor := events[limit-1].ID
		nextCursor = &cursor
		events = events[:limit]
	}
	data := make([]map[string]any, 0, len(events))
	for _, event := range events {
		data = append(data, jobEventToMap(event, traceIDFromContext(r.Context())))
	}
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "events",
		Data:       data,
		NextCursor: nextCursor,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	limit, ok := s.parseLimit(w, r, r.URL.Query().Get("limit"), 100)
	if !ok {
		return
	}

	tenantID, appID := requestScope(r)
	items, err := s.templates.List(r.Context(), templates.ListFilter{TenantID: tenantID, AppID: appID})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list templates"))
		return
	}
	data := make([]map[string]any, 0, len(items))
	for _, item := range items {
		data = append(data, templateToMap(item))
	}
	page := collectionAfterCursor(data, strings.TrimSpace(r.URL.Query().Get("cursor")))
	nextCursor := collectionNextCursor(page, limit)
	if len(page) > limit {
		page = page[:limit]
	}
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "templates",
		Data:       page,
		NextCursor: nextCursor,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

func (s *Server) handleCache(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getCacheStatus(w, r)
	case http.MethodDelete:
		s.purgeCache(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodDelete)
	}
}

func (s *Server) replayWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}

	raw, hasBody, ok := s.readOptionalBody(w, r)
	if !ok {
		return
	}
	var request webhookReplayRequest
	if hasBody && !s.decodeBody(w, r, raw, &request) {
		return
	}
	if _, ok := s.resolveAPIVersion(w, r, request.APIVersion); !ok {
		return
	}
	if !s.authorizeGatewayAction(w, r, "webhook:replay") {
		return
	}

	headerKey := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	bodyKey := strings.TrimSpace(request.IdempotencyKey)
	if headerKey != "" && bodyKey != "" && headerKey != bodyKey {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-MISMATCH-001", "idempotency_key must match Idempotency-Key"))
		return
	}
	idempotencyKey := firstNonEmpty(headerKey, bodyKey)
	if idempotencyKey == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-MISSING-001", "Idempotency-Key is required for webhook replay"))
		return
	}
	if !isIdempotencyKey(idempotencyKey) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-001", "Idempotency-Key must be 16-128 characters and contain only letters, numbers, dot, underscore, colon, or dash"))
		return
	}

	tenantID, appID := requestScope(r)
	scope := idempotency.Scope{
		TenantID:  tenantID,
		AppID:     appID,
		Operation: "webhook_replay",
		Key:       idempotencyKey,
	}
	requestHash := hashString(strings.Join([]string{r.Method, r.URL.Path, _canonicalWebhookReplayPayload(request), string(bytes.TrimSpace(raw))}, "\n"))
	decision, err := s.idempotency.Reserve(r.Context(), scope, requestHash)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to reserve idempotency key"))
		return
	}
	switch decision.Kind {
	case idempotency.DecisionConflict:
		s.writeError(w, r, http.StatusConflict, validationError("UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001", "idempotency key was replayed with a different payload"))
		return
	case idempotency.DecisionReplay:
		s.writeJSON(w, replayHTTPStatus(decision.Record, http.StatusAccepted), webhookReplayResponse{
			APIVersion:       s.apiVersion,
			Status:           "accepted",
			IdempotentReplay: true,
			WebhookID:        request.WebhookID,
			DeliveryID:       firstNonEmpty(decision.Record.ResourceID, request.DeliveryID),
			AuditEvent:       "webhook.delivery_replayed",
			Metadata: map[string]any{
				"idempotency_key":   idempotencyKey,
				"reason":            strings.TrimSpace(request.Reason),
				"original_delivery": request.DeliveryID,
			},
			TraceID: traceIDFromContext(r.Context()),
		})
		return
	}

	if strings.TrimSpace(request.DeliveryID) == "" {
		_ = s.idempotency.Release(r.Context(), scope)
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WEBHOOK-REPLAY-DELIVERY-001", "delivery_id is required for webhook replay"))
		return
	}
	if strings.TrimSpace(request.Reason) == "" {
		_ = s.idempotency.Release(r.Context(), scope)
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WEBHOOK-REPLAY-REASON-001", "reason is required for webhook replay"))
		return
	}
	replay, found, err := s.webhooks.Replay(r.Context(), tenantID, appID, request.DeliveryID, idempotencyKey, time.Now().UTC())
	if err != nil {
		_ = s.idempotency.Release(r.Context(), scope)
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to enqueue webhook replay"))
		return
	}
	if !found {
		_ = s.idempotency.Release(r.Context(), scope)
		s.writeError(w, r, http.StatusNotFound, queueError("UBAG-QUEUE-WEBHOOK-DELIVERY-NOT-FOUND-001", "webhook delivery was not found", false))
		return
	}
	resourceID := replay.ID
	if err := s.idempotency.Complete(r.Context(), scope, resourceID, http.StatusAccepted); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to complete idempotency record"))
		return
	}

	s.writeJSON(w, http.StatusAccepted, webhookReplayResponse{
		APIVersion:       s.apiVersion,
		Status:           "accepted",
		IdempotentReplay: false,
		WebhookID:        replay.EndpointID,
		DeliveryID:       replay.ID,
		AuditEvent:       "webhook.delivery_replayed",
		Metadata: map[string]any{
			"idempotency_key":   idempotencyKey,
			"reason":            strings.TrimSpace(request.Reason),
			"original_delivery": request.DeliveryID,
		},
		TraceID: traceIDFromContext(r.Context()),
	})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}

	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		s.handleWebSocketUpgrade(w, r)
		return
	}

	w.Header().Set("Upgrade", "websocket")
	s.writeError(w, r, http.StatusUpgradeRequired, validationError("UBAG-VALIDATION-WEBSOCKET-UPGRADE-001", "/v1/stream requires a WebSocket upgrade"))
}

func (s *Server) handleWebSocketUpgrade(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WEBSOCKET-KEY-001", "Sec-WebSocket-Key is required"))
		return
	}
	decodedKey, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decodedKey) != 16 {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WEBSOCKET-KEY-001", "Sec-WebSocket-Key must be a base64-encoded 16-byte nonce"))
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		s.writeError(w, r, http.StatusInternalServerError, internalError("response writer cannot upgrade websocket"))
		return
	}

	conn, buffer, err := hijacker.Hijack()
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to upgrade websocket"))
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	accept := websocketAcceptKey(key)
	_, _ = fmt.Fprintf(buffer, "HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = fmt.Fprintf(buffer, "Upgrade: websocket\r\n")
	_, _ = fmt.Fprintf(buffer, "Connection: Upgrade\r\n")
	_, _ = fmt.Fprintf(buffer, "Sec-WebSocket-Accept: %s\r\n", accept)
	_, _ = fmt.Fprintf(buffer, "Ubag-Trace-Id: %s\r\n", traceIDFromContext(r.Context()))
	_, _ = fmt.Fprintf(buffer, "\r\n")
	_ = buffer.Flush()

	welcome := map[string]any{
		"api_version": s.apiVersion,
		"type":        "stream.opened",
		"trace_id":    traceIDFromContext(r.Context()),
		"created_at":  time.Now().UTC(),
	}
	encoded, _ := json.Marshal(welcome)
	_, _ = conn.Write(websocketTextFrame(encoded))
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for sequence := 1; sequence <= 8; sequence++ {
		select {
		case <-r.Context().Done():
			return
		case now := <-ticker.C:
			heartbeat := map[string]any{
				"api_version": s.apiVersion,
				"type":        "stream.heartbeat",
				"sequence":    sequence,
				"trace_id":    traceIDFromContext(r.Context()),
				"created_at":  now.UTC(),
			}
			payload, _ := json.Marshal(heartbeat)
			if _, err := conn.Write(websocketTextFrame(payload)); err != nil {
				return
			}
		}
	}
	_, _ = conn.Write([]byte{0x88, 0x00})
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listJobs(w, r)
	case http.MethodPost:
		s.createJob(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

// handleBatchJobs implements POST /v1/jobs/batch (blueprint §10, §19.2).
// Accepts up to 100 job submissions in one HTTP round-trip and returns an
// outcome for each one. Individual failures do not abort the batch — each
// entry carries its own status and error.
func (s *Server) handleBatchJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}

	const maxBatchSize = 100
	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBody))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-READ-001", "failed to read request body"))
		return
	}

	var batchReq batchCreateJobRequest
	if err := json.Unmarshal(body, &batchReq); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JSON-001", "request body is not valid JSON"))
		return
	}
	if len(batchReq.Jobs) == 0 {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-LENGTH-001", "batch must contain at least one job"))
		return
	}
	if len(batchReq.Jobs) > maxBatchSize {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-LIMIT-001",
			fmt.Sprintf("batch size %d exceeds maximum %d", len(batchReq.Jobs), maxBatchSize)))
		return
	}

	traceID := traceIDFromContext(r.Context())
	principal, ok := principalFromContext(r.Context())
	if !ok {
		s.writeError(w, r, http.StatusUnauthorized, authError("UBAG-AUTH-MISSING-001", "authentication required"))
		return
	}
	tenantID := principal.TenantID
	appID := principal.AppID
	// Use the batch-level api_version if supplied; fall back to the server default.
	apiVersion := s.apiVersion
	if v := strings.TrimSpace(batchReq.APIVersion); v != "" {
		apiVersion = v
	}

	results := make([]batchJobOutcome, 0, len(batchReq.Jobs))
	accepted, rejected := 0, 0

	for i, req := range batchReq.Jobs {
		outcome, httpStatus := s.processBatchEntry(r.Context(), i, apiVersion, tenantID, appID, traceID, req)
		results = append(results, outcome)
		if httpStatus == http.StatusAccepted || httpStatus == http.StatusOK {
			accepted++
		} else {
			rejected++
		}
	}

	resp := batchCreateJobResponse{
		APIVersion: apiVersion,
		Results:    results,
		Accepted:   accepted,
		Rejected:   rejected,
		TraceID:    traceID,
	}
	status := http.StatusMultiStatus
	if rejected == 0 {
		status = http.StatusAccepted
	}
	s.writeJSON(w, status, resp)
}

// processBatchEntry creates and enqueues a single job within a batch.
// Returns the outcome and the HTTP status that would have been returned for a
// standalone request (used to categorise accepted vs rejected).
func (s *Server) processBatchEntry(
	ctx context.Context,
	index int,
	apiVersion, tenantID, appID, traceID string,
	req createJobRequest,
) (batchJobOutcome, int) {
	// Basic validation mirrors createJob — inline here to avoid the full HTTP
	// request/response cycle.
	target := strings.TrimSpace(req.Job.Target)
	if target == "" {
		e := validationError("UBAG-VALIDATION-JOB-TARGET-001", "job.target is required")
		return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusBadRequest
	}
	cmdType := strings.TrimSpace(req.Job.CommandType)
	if cmdType == "" {
		e := validationError("UBAG-VALIDATION-JOB-COMMAND-001", "job.command_type is required")
		return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusBadRequest
	}
	if req.Job.Input == nil {
		e := validationError("UBAG-VALIDATION-JOB-INPUT-001", "job.input must be a non-null JSON object")
		return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusBadRequest
	}
	if err := validateExecutableJobPayload(req); err != nil {
		e := validationError("UBAG-VALIDATION-JOB-PAYLOAD-SAFETY-001", err.Error())
		return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusBadRequest
	}

	// Custom-validator plugin hook (matches createJob behaviour).
	if s.plugins != nil {
		inputJSON, _ := json.Marshal(req.Job.Input)
		if result, err := s.plugins.RunHooks(ctx, "validate", inputJSON); err != nil {
			e := internalError("plugin validator error")
			return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusInternalServerError
		} else if result.Action == "reject" {
			reason := result.Reason
			if reason == "" {
				reason = "rejected by plugin validator"
			}
			e := validationError("UBAG-PLUGIN-REJECT-001", reason)
			return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusBadRequest
		}
	}
	// Pre-job plugin hook.
	if s.plugins != nil {
		hookPayload, _ := json.Marshal(req.Job)
		if result, err := s.plugins.RunHooks(ctx, "job.pre", hookPayload); err != nil {
			e := internalError("plugin pre-job hook error")
			return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusInternalServerError
		} else if result.Action == "reject" {
			reason := result.Reason
			if reason == "" {
				reason = "rejected by plugin"
			}
			e := validationError("UBAG-PLUGIN-REJECT-001", reason)
			return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusBadRequest
		}
	}

	// Auto-generate idempotency key if absent.
	idempKey := strings.TrimSpace(req.IdempotencyKey)
	if idempKey == "" {
		idempKey = generatedTraceID() // unique per entry
	}

	// §14 backpressure: reject this entry when the queue is too deep.
	if s.maxQueueDepth > 0 {
		if stats, err := s.executor.Stats(ctx); err == nil {
			pending := 0
			for _, v := range stats.DepthByState {
				pending += v
			}
			if pending >= s.maxQueueDepth {
				e := queueError("UBAG-QUEUE-BACKPRESSURE-002", "queue is too deep; retry later", true)
				e.RetryAfterMS = ptrInt(30 * 1000)
				return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusTooManyRequests
			}
		}
	}

	// §14 concurrency ceiling: acquire a token before creating the job.
	if s.concurrency != nil {
		if !s.concurrency.Acquire(tenantID, target, appID) {
			e := concurrencyError("UBAG-CONCURRENCY-001", "concurrency ceiling reached for this target", nil)
			return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusTooManyRequests
		}
	}

	job, err := s.jobs.Create(ctx, jobstore.CreateRequest{
		APIVersion:     apiVersion,
		TenantID:       tenantID,
		AppID:          appID,
		IdempotencyKey: idempKey,
		Target:         target,
		CommandType:    cmdType,
		Client:         clientToMap(req.Client),
		ConversationID: strings.TrimSpace(req.Job.ConversationID),
		TemplateID:     strings.TrimSpace(req.Job.TemplateID),
		Input:          req.Job.Input,
		Options:        req.Job.Options,
		Callbacks:      req.Job.Callbacks,
		Context:        req.Job.Context,
		TraceID:        traceID,
		NotBefore:      req.Job.NotBefore,
	})
	if err != nil {
		s.releaseConcurrencyToken(tenantID, target, appID)
		e := internalError("failed to create job")
		return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusInternalServerError
	}

	// Region-aware routing: resolve the dispatch region before enqueue.
	dispatchCtx := ctx
	if s.regionRouter != nil {
		targetRegion, routeErr := s.regionRouter.Route(ctx, tenantID)
		if routeErr != nil {
			_, _, _ = s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusFailedRetryable)
			s.releaseConcurrencyToken(tenantID, target, appID)
			e := queueError("UBAG-REGION-MISMATCH-001", "tenant home region is unavailable for routing", true)
			return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusServiceUnavailable
		}
		if targetRegion != "" {
			dispatchCtx = executor.WithDispatchRegion(ctx, string(targetRegion))
		}
	}

	if s.outbox != nil {
		env := executor.EnvelopeFromJob(job)
		envelopeBytes, err := json.Marshal(env)
		if err != nil {
			_, _, _ = s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusFailedRetryable)
			s.releaseConcurrencyToken(tenantID, target, appID)
			e := internalError("failed to marshal job envelope")
			return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusInternalServerError
		}
		if err := s.outbox.Append(dispatchCtx, job.ID, "jobs.dispatch", envelopeBytes); err != nil {
			_, _, _ = s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusFailedRetryable)
			s.releaseConcurrencyToken(tenantID, target, appID)
			e := queueError("UBAG-QUEUE-ENQUEUE-001", "failed to write job to outbox", true)
			return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusServiceUnavailable
		}
	} else {
		if _, err := s.executor.EnqueueJob(dispatchCtx, job); err != nil {
			_, _, _ = s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusFailedRetryable)
			s.releaseConcurrencyToken(tenantID, target, appID)
			e := queueError("UBAG-QUEUE-ENQUEUE-001", "failed to enqueue job", true)
			return batchJobOutcome{Index: index, Status: "rejected", Error: &e}, http.StatusServiceUnavailable
		}
	}

	return batchJobOutcome{Index: index, Status: "accepted", JobID: job.ID}, http.StatusAccepted
}

func (s *Server) handleJobByID(w http.ResponseWriter, r *http.Request) {
	segments := splitRouteTail(r.URL.Path, "/v1/jobs/")
	if len(segments) == 0 || segments[0] == "" {
		s.writeNotFound(w, r)
		return
	}

	switch {
	case len(segments) == 1 && r.Method == http.MethodGet:
		s.getJob(w, r, segments[0])
	// DELETE /v1/jobs/{id} — hard cancel: cooperative signal + immediate status force
	case len(segments) == 1 && r.Method == http.MethodDelete:
		s.cancelJob(w, r, segments[0])
	case len(segments) == 2 && segments[1] == "events" && r.Method == http.MethodGet:
		s.listJobEvents(w, r, segments[0])
	case len(segments) == 2 && segments[1] == "cancel" && r.Method == http.MethodPost:
		s.cancelJob(w, r, segments[0])
	case len(segments) == 2 && segments[1] == "retry" && r.Method == http.MethodPost:
		s.retryJob(w, r, segments[0])
	// artifact collection: GET /v1/jobs/{id}/artifacts
	// artifact upload:     PUT /v1/jobs/{id}/artifacts/{key}
	// artifact download:   GET /v1/jobs/{id}/artifacts/{key}
	// artifact delete:    DELETE /v1/jobs/{id}/artifacts/{key}
	case len(segments) == 2 && segments[1] == "artifacts" && r.Method == http.MethodGet:
		s.listJobArtifacts(w, r, segments[0])
	case len(segments) == 3 && segments[1] == "artifacts" && r.Method == http.MethodPut:
		s.putJobArtifact(w, r, segments[0], segments[2])
	case len(segments) == 3 && segments[1] == "artifacts" && r.Method == http.MethodGet:
		s.getJobArtifact(w, r, segments[0], segments[2])
	case len(segments) == 3 && segments[1] == "artifacts" && r.Method == http.MethodDelete:
		s.deleteJobArtifact(w, r, segments[0], segments[2])
	case len(segments) == 2 && segments[1] == "artifacts":
		s.writeMethodNotAllowed(w, r, http.MethodGet)
	case len(segments) == 3 && segments[1] == "artifacts":
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPut, http.MethodDelete)
	case len(segments) == 1:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodDelete)
	case len(segments) == 2 && (segments[1] == "events"):
		s.writeMethodNotAllowed(w, r, http.MethodGet)
	case len(segments) == 2 && (segments[1] == "cancel" || segments[1] == "retry"):
		s.writeMethodNotAllowed(w, r, http.MethodPost)
	default:
		s.writeNotFound(w, r)
	}
}

func (s *Server) handleJobSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}

	segments := splitRouteTail(r.URL.Path, "/v1/sse/jobs/")
	if len(segments) != 1 || segments[0] == "" {
		s.writeNotFound(w, r)
		return
	}

	job, ok := s.loadAuthorizedJob(w, r, segments[0], "job:read")
	if !ok {
		return
	}

	events, found, err := s.jobs.ListEvents(r.Context(), job.ID, 0, 100)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load job events"))
		return
	}
	if !found {
		s.writeJobNotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	s.incrementSSEConnections()
	defer s.decrementSSEConnections()

	afterSequence := 0
	for _, event := range events {
		payload, _ := json.Marshal(jobEventToResponse(event, traceIDFromContext(r.Context())))
		_, _ = fmt.Fprintf(w, "id: %s\nevent: job.%s\ndata: %s\n\n", event.ID, event.Type, payload)
		afterSequence = event.Sequence
	}
	if flusher != nil {
		flusher.Flush()
	}
	if strings.EqualFold(r.URL.Query().Get("snapshot"), "true") {
		return
	}

	for {
		nextEvents, _, err := s.jobs.WaitEvents(r.Context(), job.ID, afterSequence, 100)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			return
		}
		for _, event := range nextEvents {
			payload, _ := json.Marshal(jobEventToResponse(event, traceIDFromContext(r.Context())))
			_, _ = fmt.Fprintf(w, "id: %s\nevent: job.%s\ndata: %s\n\n", event.ID, event.Type, payload)
			afterSequence = event.Sequence
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}

	var request createJobRequest
	if !s.decodeBody(w, r, raw, &request) {
		return
	}

	apiVersion, ok := s.resolveAPIVersion(w, r, request.APIVersion)
	if !ok {
		return
	}

	tenantID, appID := requestScope(r)
	if !s.applyTemplateForCreate(w, r, tenantID, appID, &request) {
		return
	}
	if !isTargetKey(request.Job.Target) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JOB-TARGET-001", "job.target is required and must match ^[a-z0-9][a-z0-9._-]*$"))
		return
	}
	if !isTargetKey(request.Job.CommandType) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JOB-COMMAND-001", "job.command_type is required and must match ^[a-z0-9][a-z0-9._-]*$"))
		return
	}
	headerKey := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	bodyKey := strings.TrimSpace(request.IdempotencyKey)
	if headerKey != "" && bodyKey != "" && headerKey != bodyKey {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-MISMATCH-001", "idempotency_key must match Idempotency-Key"))
		return
	}

	idempotencyKey := firstNonEmpty(headerKey, bodyKey)
	if idempotencyKey == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-MISSING-001", "Idempotency-Key is required for job creation"))
		return
	}
	if !isIdempotencyKey(idempotencyKey) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-001", "Idempotency-Key must be 16-128 characters and contain only letters, numbers, dot, underscore, colon, or dash"))
		return
	}
	if strings.TrimSpace(request.Client.AppID) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-CLIENT-APP-ID-001", "client.app_id is required"))
		return
	}
	if strings.TrimSpace(request.Client.AppVersion) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-CLIENT-APP-VERSION-001", "client.app_version is required"))
		return
	}
	if strings.TrimSpace(request.Client.SDK.Name) == "" || strings.TrimSpace(request.Client.SDK.Version) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-CLIENT-SDK-001", "client.sdk.name and client.sdk.version are required"))
		return
	}
	if request.Job.Input == nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JOB-INPUT-001", "job.input is required and must be an object"))
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}
	if s.killSwitch != nil && !s.killSwitch.IsAcceptingJobs(r.Context()) {
		w.Header().Set("Retry-After", "60")
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-REGION-DRAINING-001", "this region is not accepting new jobs; try another region or retry later", true))
		return
	}
	if _, _, err := webhooks.CallbackFromMap(request.Job.Callbacks, s.webhookURLs); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WEBHOOK-CALLBACK-001", err.Error()))
		return
	}
	if err := validateExecutableJobPayload(request); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JOB-PAYLOAD-SAFETY-001", err.Error()))
		return
	}

	// Custom-validator plugin hook: validate job input before proceeding.
	if s.plugins != nil {
		inputJSON, _ := json.Marshal(request.Job.Input)
		if result, err := s.plugins.RunHooks(r.Context(), "validate", inputJSON); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("plugin validator error"))
			return
		} else if result.Action == "reject" {
			reason := result.Reason
			if reason == "" {
				reason = "rejected by plugin validator"
			}
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-PLUGIN-REJECT-001", reason))
			return
		}
	}

	// Pre-job plugin hook: runs before the job is created.
	if s.plugins != nil {
		hookPayload, _ := json.Marshal(request.Job)
		if result, err := s.plugins.RunHooks(r.Context(), "job.pre", hookPayload); err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("plugin pre-job hook error"))
			return
		} else if result.Action == "reject" {
			reason := result.Reason
			if reason == "" {
				reason = "rejected by plugin"
			}
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-PLUGIN-REJECT-001", reason))
			return
		}
	}

	requestHash, err := canonicalCreateJobHash(apiVersion, request)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JSON-001", "request body must be valid JSON"))
		return
	}

	scope := idempotency.Scope{
		TenantID:  tenantID,
		AppID:     appID,
		Operation: "create_job",
		Key:       idempotencyKey,
	}

	decision, err := s.idempotency.Reserve(r.Context(), scope, requestHash)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to reserve idempotency key"))
		return
	}

	switch decision.Kind {
	case idempotency.DecisionConflict:
		s.writeError(w, r, http.StatusConflict, validationError("UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001", "idempotency key was replayed with a different payload"))
		return
	case idempotency.DecisionReplay:
		s.idempotencyReplays.Add(1) // ubag_idempotency_replays_total
		s.replayJob(w, r, decision.Record)
		return
	}

	// §14 backpressure: reject new jobs when the queue is too deep.
	if s.maxQueueDepth > 0 {
		stats, err := s.executor.Stats(r.Context())
		if err == nil {
			pending := 0
			for _, v := range stats.DepthByState {
				pending += v
			}
			if pending >= s.maxQueueDepth {
				const retryAfterSecs = 30
				_ = s.idempotency.Release(r.Context(), scope)
				w.Header().Set("Retry-After", strconv.Itoa(retryAfterSecs))
				errObj := queueError("UBAG-QUEUE-BACKPRESSURE-002", "queue is too deep; retry later", true)
				errObj.RetryAfterMS = ptrInt(retryAfterSecs * 1000)
				s.writeError(w, r, http.StatusTooManyRequests, errObj)
				return
			}
		}
	}

	// §14 concurrency ceiling: acquire a token before creating the job.
	if s.concurrency != nil {
		if !s.concurrency.Acquire(tenantID, request.Job.Target, appID) {
			_ = s.idempotency.Release(r.Context(), scope)
			s.writeError(w, r, http.StatusTooManyRequests, concurrencyError("UBAG-CONCURRENCY-001", "concurrency ceiling reached for this target", nil))
			return
		}
	}

	job, err := s.jobs.Create(r.Context(), jobstore.CreateRequest{
		APIVersion:     apiVersion,
		TenantID:       tenantID,
		AppID:          appID,
		IdempotencyKey: idempotencyKey,
		Target:         strings.TrimSpace(request.Job.Target),
		CommandType:    strings.TrimSpace(request.Job.CommandType),
		Client:         clientToMap(request.Client),
		ConversationID: strings.TrimSpace(request.Job.ConversationID),
		TemplateID:     strings.TrimSpace(request.Job.TemplateID),
		Input:          request.Job.Input,
		Options:        request.Job.Options,
		Callbacks:      request.Job.Callbacks,
		Context:        request.Job.Context,
		TraceID:        traceIDFromContext(r.Context()),
		NotBefore:      request.Job.NotBefore,
	})
	if err != nil {
		_ = s.idempotency.Release(r.Context(), scope)
		s.releaseConcurrencyToken(tenantID, request.Job.Target, appID)
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to create job"))
		return
	}

	// Region-aware routing: resolve the dispatch region before enqueue.
	dispatchCtx := r.Context()
	if s.regionRouter != nil {
		targetRegion, routeErr := s.regionRouter.Route(dispatchCtx, tenantID)
		if routeErr != nil {
			_, _, _ = s.jobs.UpdateStatus(r.Context(), job.ID, jobstore.StatusFailedRetryable)
			_ = s.idempotency.Release(r.Context(), scope)
			s.releaseConcurrencyToken(tenantID, request.Job.Target, appID)
			s.writeError(w, r, http.StatusServiceUnavailable,
				queueError("UBAG-REGION-MISMATCH-001", "tenant home region is unavailable for routing", true))
			return
		}
		if targetRegion != "" {
			dispatchCtx = executor.WithDispatchRegion(dispatchCtx, string(targetRegion))
		}
	}

	if s.outbox != nil {
		env := executor.EnvelopeFromJob(job)
		envelopeBytes, err := json.Marshal(env)
		if err != nil {
			_, _, _ = s.jobs.UpdateStatus(r.Context(), job.ID, jobstore.StatusFailedRetryable)
			_ = s.idempotency.Release(r.Context(), scope)
			s.releaseConcurrencyToken(tenantID, request.Job.Target, appID)
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to marshal job envelope"))
			return
		}
		// Outbox path: write to the reliable local buffer — the relay dispatcher handles
		// the actual enqueue, and the breaker wraps the relay's EnqueueJob call.
		// No breaker check is needed here; the outbox write itself cannot be circuit-broken.
		if err := s.outbox.Append(r.Context(), job.ID, "jobs.dispatch", envelopeBytes); err != nil {
			_, _, _ = s.jobs.UpdateStatus(r.Context(), job.ID, jobstore.StatusFailedRetryable)
			_ = s.idempotency.Release(r.Context(), scope)
			s.releaseConcurrencyToken(tenantID, request.Job.Target, appID)
			s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-ENQUEUE-001", "failed to write job to outbox", true))
			return
		}
	} else {
		if _, err := s.executor.EnqueueJob(dispatchCtx, job); err != nil {
			_, _, _ = s.jobs.UpdateStatus(r.Context(), job.ID, jobstore.StatusFailedRetryable)
			_ = s.idempotency.Release(r.Context(), scope)
			s.releaseConcurrencyToken(tenantID, request.Job.Target, appID)
			var breakerErr *resilience.BreakerOpenError
			if errors.As(err, &breakerErr) {
				retryAfterSecs := int(math.Ceil(breakerErr.RetryAfter.Seconds()))
				if retryAfterSecs < 1 {
					retryAfterSecs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retryAfterSecs))
				s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-BREAKER-OPEN-001", breakerErr.Error(), true))
				return
			}
			s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-ENQUEUE-001", "failed to enqueue job for execution", true))
			return
		}
	}

	if err := s.idempotency.Complete(r.Context(), scope, job.ID, http.StatusAccepted); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to complete idempotency record"))
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v1/jobs/%s", job.ID))
	s.writeJSON(w, http.StatusAccepted, jobToResponse(job, false, traceIDFromContext(r.Context())))
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}

	query := r.URL.Query()
	limit, ok := s.parseLimit(w, r, query.Get("limit"), 100)
	if !ok {
		return
	}
	status := strings.TrimSpace(query.Get("filter[status]"))
	if status != "" && !jobstore.KnownStatus(jobstore.Status(status)) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JOB-STATUS-001", "filter[status] is not supported"))
		return
	}
	target := strings.TrimSpace(query.Get("filter[target]"))
	if target != "" && !isTargetKey(target) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JOB-TARGET-001", "filter[target] must match ^[a-z0-9][a-z0-9._-]*$"))
		return
	}
	sortParam := strings.TrimSpace(query.Get("sort"))
	if sortParam != "" && sortParam != "created_at" && sortParam != "-created_at" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JOB-SORT-001", "sort must be created_at or -created_at"))
		return
	}

	tenantID, appID := requestScope(r)
	jobs, err := s.jobs.List(r.Context(), jobstore.ListFilter{TenantID: tenantID, AppID: appID, Status: status, Target: target})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list jobs"))
		return
	}

	sortJobs(jobs, sortParam)
	cursor := strings.TrimSpace(query.Get("cursor"))
	if cursor != "" {
		jobs = jobsAfterCursor(jobs, cursor)
	}
	nextCursor := (*string)(nil)
	if len(jobs) > limit {
		cursorValue := jobs[limit-1].ID
		nextCursor = &cursorValue
		jobs = jobs[:limit]
	}

	responses := make([]jobResponse, 0, len(jobs))
	for _, job := range jobs {
		responses = append(responses, jobToResponse(job, false, traceIDFromContext(r.Context())))
	}

	s.writeJSON(w, http.StatusOK, listJobsResponse{
		APIVersion: s.apiVersion,
		Jobs:       responses,
		NextCursor: nextCursor,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request, id string) {
	job, ok := s.loadAuthorizedJob(w, r, id, "job:read")
	if !ok {
		return
	}

	s.writeJSON(w, http.StatusOK, jobToResponse(job, false, traceIDFromContext(r.Context())))
}

func (s *Server) listJobEvents(w http.ResponseWriter, r *http.Request, id string) {
	job, ok := s.loadAuthorizedJob(w, r, id, "job:read")
	if !ok {
		return
	}

	limit, ok := s.parseLimit(w, r, r.URL.Query().Get("limit"), 100)
	if !ok {
		return
	}
	afterSequence := 0
	rawAfter := strings.TrimSpace(r.URL.Query().Get("after_sequence"))
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawAfter != "" && rawCursor != "" && rawAfter != rawCursor {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-EVENT-SEQUENCE-001", "cursor and after_sequence must match when both are supplied"))
		return
	}
	rawSequence := rawAfter
	sequenceParam := "after_sequence"
	if rawSequence == "" {
		rawSequence = rawCursor
		sequenceParam = "cursor"
	}
	if rawSequence != "" {
		parsed, err := strconv.Atoi(rawSequence)
		if err != nil || parsed < 0 {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-EVENT-SEQUENCE-001", sequenceParam+" must be a non-negative integer"))
			return
		}
		afterSequence = parsed
	}

	events, found, err := s.jobs.ListEvents(r.Context(), id, afterSequence, limit+1)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load job events"))
		return
	}
	if !found {
		s.writeJobNotFound(w, r)
		return
	}
	nextCursor := (*string)(nil)
	if len(events) > limit {
		cursorValue := strconv.Itoa(events[limit-1].Sequence)
		nextCursor = &cursorValue
		events = events[:limit]
	}
	responses := make([]jobEventResponse, 0, len(events))
	for _, event := range events {
		responses = append(responses, jobEventToResponse(event, traceIDFromContext(r.Context())))
	}

	s.writeJSON(w, http.StatusOK, jobEventsResponse{
		APIVersion: job.APIVersion,
		JobID:      job.ID,
		Events:     responses,
		NextCursor: nextCursor,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, id string) {
	existing, ok := s.loadAuthorizedJob(w, r, id, "job:cancel")
	if !ok {
		return
	}

	mutation, ok := s.reserveMutation(w, r, "cancel_job", id)
	if !ok {
		return
	}
	if mutation.replay {
		s.replayJob(w, r, mutation.record)
		return
	}

	reason := firstNonEmpty(mutation.reason, "caller_cancelled")
	if err := s.executor.CancelJob(r.Context(), existing, reason); err != nil {
		_ = s.idempotency.Release(r.Context(), mutation.scope)
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-CANCEL-001", "failed to cancel job execution", true))
		return
	}

	job, found, err := s.jobs.UpdateStatus(r.Context(), id, jobstore.StatusCanceled)
	if err != nil {
		_ = s.idempotency.Release(r.Context(), mutation.scope)
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to cancel job"))
		return
	}
	if !found {
		_ = s.idempotency.Release(r.Context(), mutation.scope)
		s.writeJobNotFound(w, r)
		return
	}
	if jobstore.TerminalStatus(job.Status) {
		// Release the concurrency token on hard cancel.
		s.releaseConcurrencyToken(job.TenantID, job.Target, job.AppID)
		notifier := webhooks.JobOutbox{Store: s.webhooks, URLPolicy: s.webhookURLs}
		if err := notifier.EnqueueTerminalJob(r.Context(), job); err != nil {
			_ = s.idempotency.Release(r.Context(), mutation.scope)
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to enqueue webhook delivery"))
			return
		}
	}

	if err := s.idempotency.Complete(r.Context(), mutation.scope, job.ID, http.StatusAccepted); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to complete idempotency record"))
		return
	}

	s.writeJSON(w, http.StatusAccepted, jobToResponse(job, false, traceIDFromContext(r.Context())))
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request, id string) {
	original, ok := s.loadAuthorizedJob(w, r, id, "job:retry")
	if !ok {
		return
	}

	mutation, ok := s.reserveMutation(w, r, "retry_job", id)
	if !ok {
		return
	}
	if mutation.replay {
		s.replayJob(w, r, mutation.record)
		return
	}

	job, err := s.jobs.Create(r.Context(), jobstore.CreateRequest{
		APIVersion:     original.APIVersion,
		TenantID:       original.TenantID,
		AppID:          original.AppID,
		IdempotencyKey: mutation.scope.Key,
		Target:         original.Target,
		CommandType:    original.CommandType,
		Client:         original.Client,
		ConversationID: original.ConversationID,
		TemplateID:     original.TemplateID,
		Input:          original.Input,
		Options:        original.Options,
		Callbacks:      original.Callbacks,
		Context:        original.Context,
		TraceID:        traceIDFromContext(r.Context()),
		RetryOf:        original.ID,
	})
	if err != nil {
		_ = s.idempotency.Release(r.Context(), mutation.scope)
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to retry job"))
		return
	}
	if _, err := s.executor.EnqueueJob(r.Context(), job); err != nil {
		_, _, _ = s.jobs.UpdateStatus(r.Context(), job.ID, jobstore.StatusFailedRetryable)
		_ = s.idempotency.Release(r.Context(), mutation.scope)
		var breakerErr *resilience.BreakerOpenError
		if errors.As(err, &breakerErr) {
			retryAfterSecs := int(math.Ceil(breakerErr.RetryAfter.Seconds()))
			if retryAfterSecs < 1 {
				retryAfterSecs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSecs))
			s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-BREAKER-OPEN-001", breakerErr.Error(), true))
			return
		}
		s.writeError(w, r, http.StatusServiceUnavailable, queueError("UBAG-QUEUE-ENQUEUE-001", "failed to enqueue retry job for execution", true))
		return
	}

	if err := s.idempotency.Complete(r.Context(), mutation.scope, job.ID, http.StatusAccepted); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to complete idempotency record"))
		return
	}

	s.writeJSON(w, http.StatusAccepted, jobToResponse(job, false, traceIDFromContext(r.Context())))
}

func (s *Server) replayJob(w http.ResponseWriter, r *http.Request, record idempotency.Record) {
	if record.ResourceID == "" {
		s.writeError(w, r, http.StatusConflict, validationError("UBAG-VALIDATION-IDEMPOTENCY-IN-PROGRESS-001", "idempotent operation is still in progress"))
		return
	}

	job, ok, err := s.jobs.Get(r.Context(), record.ResourceID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load idempotent job"))
		return
	}
	if !ok {
		s.writeError(w, r, http.StatusInternalServerError, internalError("idempotency record points to a missing job"))
		return
	}

	status := record.HTTPStatus
	if status == 0 {
		status = http.StatusOK
	}

	s.writeJSON(w, status, jobToResponse(job, true, traceIDFromContext(r.Context())))
}

type mutationReservation struct {
	scope  idempotency.Scope
	record idempotency.Record
	replay bool
	reason string
}

func (s *Server) reserveMutation(w http.ResponseWriter, r *http.Request, operation, resourceID string) (mutationReservation, bool) {
	var request jobMutationRequest
	raw, hasBody, ok := s.readOptionalBody(w, r)
	if !ok {
		return mutationReservation{}, false
	}
	if hasBody && !s.decodeBody(w, r, raw, &request) {
		return mutationReservation{}, false
	}
	if request.JobID != "" && request.JobID != resourceID {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JOB-ID-MISMATCH-001", "request job_id must match route job_id"))
		return mutationReservation{}, false
	}
	if _, ok := s.resolveAPIVersion(w, r, request.APIVersion); !ok {
		return mutationReservation{}, false
	}

	headerKey := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	bodyKey := strings.TrimSpace(request.IdempotencyKey)
	if headerKey != "" && bodyKey != "" && headerKey != bodyKey {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-MISMATCH-001", "idempotency_key must match Idempotency-Key"))
		return mutationReservation{}, false
	}

	idempotencyKey := firstNonEmpty(headerKey, bodyKey)
	if idempotencyKey == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-MISSING-001", "Idempotency-Key is required for mutating job routes"))
		return mutationReservation{}, false
	}
	if !isIdempotencyKey(idempotencyKey) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-001", "Idempotency-Key must be 16-128 characters and contain only letters, numbers, dot, underscore, colon, or dash"))
		return mutationReservation{}, false
	}

	tenantID, appID := requestScope(r)
	scope := idempotency.Scope{
		TenantID:  tenantID,
		AppID:     appID,
		Operation: operation,
		Key:       idempotencyKey,
	}
	requestHash, err := canonicalMutationHash(r.Method, r.URL.Path, operation, resourceID, request)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JSON-001", "request body must be valid JSON"))
		return mutationReservation{}, false
	}

	decision, err := s.idempotency.Reserve(r.Context(), scope, requestHash)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to reserve idempotency key"))
		return mutationReservation{}, false
	}

	switch decision.Kind {
	case idempotency.DecisionConflict:
		s.writeError(w, r, http.StatusConflict, validationError("UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001", "idempotency key was replayed with a different payload"))
		return mutationReservation{}, false
	case idempotency.DecisionReplay:
		return mutationReservation{scope: scope, record: decision.Record, replay: true, reason: strings.TrimSpace(request.Reason)}, true
	default:
		return mutationReservation{scope: scope, record: decision.Record, reason: strings.TrimSpace(request.Reason)}, true
	}
}

func (s *Server) reserveArtifactMutation(w http.ResponseWriter, r *http.Request, operation string, jobID string, key string, requestHash string) (mutationReservation, bool) {
	idempotencyKey := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	if idempotencyKey == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-MISSING-001", "Idempotency-Key is required for mutating artifact routes"))
		return mutationReservation{}, false
	}
	if !isIdempotencyKey(idempotencyKey) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-001", "Idempotency-Key must be 16-128 characters and contain only letters, numbers, dot, underscore, colon, or dash"))
		return mutationReservation{}, false
	}
	tenantID, appID := requestScope(r)
	scope := idempotency.Scope{
		TenantID:  tenantID,
		AppID:     appID,
		Operation: operation,
		Key:       idempotencyKey,
	}
	decision, err := s.idempotency.Reserve(r.Context(), scope, requestHash)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to reserve idempotency key"))
		return mutationReservation{}, false
	}
	switch decision.Kind {
	case idempotency.DecisionConflict:
		s.writeError(w, r, http.StatusConflict, validationError("UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001", "idempotency key was replayed with a different artifact mutation"))
		return mutationReservation{}, false
	case idempotency.DecisionReplay:
		return mutationReservation{scope: scope, record: decision.Record, replay: true}, true
	default:
		return mutationReservation{scope: scope, record: decision.Record}, true
	}
}

func (s *Server) replayPutArtifact(w http.ResponseWriter, r *http.Request, jobID string, key string) {
	rc, rec, err := s.artifactSt.GetArtifact(r.Context(), jobID, key)
	if err != nil {
		if artifacts.IsNotFound(err) {
			s.writeError(w, r, http.StatusConflict, validationError("UBAG-VALIDATION-IDEMPOTENCY-IN-PROGRESS-001", "idempotent artifact upload is not available for replay"))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to replay artifact upload"))
		return
	}
	_ = rc.Close()
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"api_version":       s.apiVersion,
		"artifact":          artifactRecordToResponse(rec),
		"idempotent_replay": true,
		"trace_id":          traceIDFromContext(r.Context()),
	})
}

func (s *Server) resolveAPIVersion(w http.ResponseWriter, r *http.Request, bodyVersion string) (string, bool) {
	headerVersion := strings.TrimSpace(r.Header.Get(headerAPIVersion))
	bodyVersion = strings.TrimSpace(bodyVersion)

	switch {
	case headerVersion == "" && bodyVersion == "":
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-API-VERSION-001", "api_version or Ubag-Api-Version is required"))
		return "", false
	case headerVersion != "" && bodyVersion != "" && headerVersion != bodyVersion:
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-API-VERSION-MISMATCH-001", "api_version must match Ubag-Api-Version"))
		return "", false
	case headerVersion != "":
		if !s.isSupportedAPIVersion(headerVersion) {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-API-VERSION-UNSUPPORTED-001", "requested API version is not supported"))
			return "", false
		}
		return headerVersion, true
	default:
		if !s.isSupportedAPIVersion(bodyVersion) {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-API-VERSION-UNSUPPORTED-001", "requested API version is not supported"))
			return "", false
		}
		return bodyVersion, true
	}
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body := http.MaxBytesReader(w, r.Body, s.maxBody)
	raw, err := io.ReadAll(body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			s.writeError(w, r, http.StatusRequestEntityTooLarge, validationError("UBAG-VALIDATION-BODY-TOO-LARGE-001", "request body exceeds gateway limit"))
			return nil, false
		}

		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-READ-001", "request body could not be read"))
		return nil, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JSON-001", "request body must be valid JSON"))
		return nil, false
	}

	return raw, true
}

func (s *Server) readOptionalBody(w http.ResponseWriter, r *http.Request) ([]byte, bool, bool) {
	body := http.MaxBytesReader(w, r.Body, s.maxBody)
	raw, err := io.ReadAll(body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			s.writeError(w, r, http.StatusRequestEntityTooLarge, validationError("UBAG-VALIDATION-BODY-TOO-LARGE-001", "request body exceeds gateway limit"))
			return nil, false, false
		}

		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-READ-001", "request body could not be read"))
		return nil, false, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false, true
	}

	return raw, true, true
}

func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, raw []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JSON-001", "request body must be valid JSON"))
		return false
	}

	return true
}

func (s *Server) writeMethodNotAllowed(w http.ResponseWriter, r *http.Request, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	s.writeError(w, r, http.StatusMethodNotAllowed, validationError("UBAG-VALIDATION-METHOD-001", "method is not allowed for this route"))
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.writeNotFound(w, r)
}

func (s *Server) writeNotFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotFound, validationError("UBAG-VALIDATION-ROUTE-001", "route was not found"))
}

func (s *Server) writeJobNotFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotFound, queueError("UBAG-QUEUE-JOB-NOT-FOUND-001", "job was not found", false))
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Ubag-Api-Version-Used", s.apiVersion)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// releaseConcurrencyToken releases a previously acquired concurrency token.
// It is nil-safe and a no-op when concurrency enforcement is not configured.
func (s *Server) releaseConcurrencyToken(tenantID, target, identityRef string) {
	if s.concurrency != nil {
		s.concurrency.Release(tenantID, target, identityRef)
	}
}

func canonicalHash(raw []byte) (string, error) {
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", err
	}

	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	return hashBytes(canonical), nil
}

func canonicalCreateJobHash(apiVersion string, request createJobRequest) (string, error) {
	return jobcore.CanonicalCreateHash(apiVersion, jobcoreClient(request.Client), jobcoreSpec(request.Job))
}

func canonicalMutationHash(method string, path string, operation string, resourceID string, request jobMutationRequest) (string, error) {
	payload := map[string]any{
		"method":      method,
		"path":        path,
		"operation":   operation,
		"resource_id": resourceID,
		"api_version": strings.TrimSpace(request.APIVersion),
		"job_id":      strings.TrimSpace(request.JobID),
		"reason":      strings.TrimSpace(request.Reason),
		"metadata":    request.Metadata,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

func canonicalArtifactMutationHash(method string, path string, operation string, jobID string, key string, contentType string, body []byte) string {
	payload := map[string]any{
		"method":       method,
		"path":         path,
		"operation":    operation,
		"job_id":       strings.TrimSpace(jobID),
		"key":          strings.TrimSpace(key),
		"content_type": strings.TrimSpace(contentType),
		"body_hash":    hashBytes(body),
	}
	encoded, _ := json.Marshal(payload)
	return hashBytes(encoded)
}

func artifactResourceID(jobID string, key string) string {
	return strings.TrimSpace(jobID) + ":" + strings.TrimSpace(key)
}

func hashString(value string) string {
	return hashBytes([]byte(value))
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func splitRouteTail(path, prefix string) []string {
	tail := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if tail == "" {
		return nil
	}

	return strings.Split(tail, "/")
}

func requestScope(r *http.Request) (string, string) {
	if principal, ok := principalFromContext(r.Context()); ok {
		return principal.TenantID, principal.AppID
	}
	return defaultTenantID, defaultAppID
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}

	return ""
}

func (s *Server) isSupportedAPIVersion(value string) bool {
	return apiVersionPattern.MatchString(value) && value == s.apiVersion
}

func isIdempotencyKey(value string) bool {
	return idempotencyKeyPattern.MatchString(strings.TrimSpace(value))
}

func isTargetKey(value string) bool {
	return targetPattern.MatchString(strings.TrimSpace(value))
}

func (s *Server) parseLimit(w http.ResponseWriter, r *http.Request, raw string, defaultLimit int) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return defaultLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 100 {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-LIMIT-001", "limit must be an integer from 1 to 100"))
		return 0, false
	}
	return limit, true
}

func jobToResponse(job jobstore.Job, replay bool, traceID string) jobResponse {
	// Build the structured §6.2 result envelope from whatever the job store
	// holds. If the worker has populated dedicated output fields we use them;
	// if it returned an opaque map we attempt to project it into the envelope.
	result := buildJobResultEnvelope(job)

	// Build the structured §6.2 metadata envelope.
	meta := JobMetadataEnvelope{
		CommandType:    job.CommandType,
		AppID:          job.AppID,
		TenantID:       job.TenantID,
		Retries:        0, // populated by Phase 2b when retry count is tracked in the job store
		ConversationID: job.ConversationID,
		TemplateID:     job.TemplateID,
		Client:         job.Client,
	}
	if job.Input != nil {
		meta.Input = job.Input
	}
	if job.Options != nil {
		meta.Options = job.Options
	}
	if job.Callbacks != nil {
		meta.Callbacks = webhooks.RedactCallbacks(job.Callbacks)
	}
	if job.Context != nil {
		meta.Context = job.Context
	}
	if job.RetryOf != "" {
		meta.RetryOf = job.RetryOf
	}

	return jobResponse{
		APIVersion:       job.APIVersion,
		JobID:            job.ID,
		IdempotentReplay: replay,
		Status:           string(job.Status),
		Target:           job.Target,
		Result:           result,
		Metadata:         meta,
		TraceID:          traceID,
		EventsURL:        fmt.Sprintf("/v1/jobs/%s/events", job.ID),
		CreatedAt:        job.CreatedAt,
		UpdatedAt:        job.UpdatedAt,
	}
}

// buildJobResultEnvelope converts the job store's opaque Result value into the
// structured §6.2 result envelope. The worker populates Result as either a
// map[string]any or nil; future workers may populate the dedicated output fields
// in the blueprint §22 schema directly.
func buildJobResultEnvelope(job jobstore.Job) *JobResultEnvelope {
	env := &JobResultEnvelope{}

	// Attempt to project a map-typed result into the structured envelope.
	if m, ok := job.Result.(map[string]any); ok && m != nil {
		if out, ok := m["output"].(map[string]any); ok {
			o := &JobOutput{}
			if v, ok := out["text"].(string); ok {
				o.Text = v
			}
			if v, ok := out["markdown"].(string); ok {
				o.Markdown = v
			}
			if v, ok := out["plain_text"].(string); ok {
				o.PlainText = v
			}
			if v, ok := out["sections"].(map[string]any); ok {
				o.Sections = v
			}
			if v, ok := out["html"].(string); ok {
				o.HTML = v
			}
			env.Output = o
		} else if text, ok := m["text"].(string); ok {
			// Flat result from a simple worker — promote to output.text.
			env.Output = &JobOutput{Text: text}
		}
		if cached, ok := m["cached"].(bool); ok {
			env.Cached = cached
		}
		if src, ok := m["cache_source"].(string); ok {
			env.CacheSource = &src
		}
	}

	// Nil result is valid (job not yet completed); return a non-nil envelope
	// so the response always has a "result" field.
	return env
}

func jobEventToResponse(event jobstore.Event, traceID string) jobEventResponse {
	if traceID == "" {
		traceID = event.TraceID
	}
	return jobEventResponse{
		EventID:    event.ID,
		JobID:      event.JobID,
		APIVersion: event.APIVersion,
		Type:       event.Type,
		CreatedAt:  event.CreatedAt,
		Sequence:   event.Sequence,
		Data:       event.Data,
		TraceID:    traceID,
	}
}

func jobEventToMap(event jobstore.Event, traceID string) map[string]any {
	if traceID == "" {
		traceID = event.TraceID
	}
	return map[string]any{
		"event_id":    event.ID,
		"job_id":      event.JobID,
		"api_version": event.APIVersion,
		"type":        event.Type,
		"created_at":  event.CreatedAt,
		"sequence":    event.Sequence,
		"data":        event.Data,
		"trace_id":    traceID,
	}
}

func templateToMap(item templates.Template) map[string]any {
	return map[string]any{
		"id":               item.ID,
		"name":             item.Name,
		"description":      item.Description,
		"target":           item.Target,
		"command_type":     item.CommandType,
		"input_defaults":   cloneMap(item.InputDefaults),
		"options_defaults": cloneMap(item.OptionsDefaults),
		"sensitive":        item.Sensitive,
		"created_at":       item.CreatedAt,
		"updated_at":       item.UpdatedAt,
	}
}

func clientToMap(client clientRequest) map[string]any {
	return jobcore.ClientToMap(jobcoreClient(client))
}

func jobcoreClient(client clientRequest) jobcore.Client {
	return jobcore.Client{
		AppID:      client.AppID,
		AppVersion: client.AppVersion,
		DeviceID:   client.DeviceID,
		UserRef:    client.UserRef,
		SDKName:    client.SDK.Name,
		SDKVersion: client.SDK.Version,
	}
}

func jobcoreSpec(job jobRequest) jobcore.Spec {
	return jobcore.Spec{
		Target:         job.Target,
		CommandType:    job.CommandType,
		ConversationID: job.ConversationID,
		TemplateID:     job.TemplateID,
		Input:          job.Input,
		Options:        job.Options,
		Callbacks:      job.Callbacks,
		Context:        job.Context,
	}
}

func (s *Server) applyTemplateForCreate(w http.ResponseWriter, r *http.Request, tenantID string, appID string, request *createJobRequest) bool {
	templateID := strings.TrimSpace(request.Job.TemplateID)
	if templateID == "" {
		return true
	}
	item, found, err := s.templates.GetScoped(r.Context(), templateID, tenantID, appID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load template"))
		return false
	}
	if !found {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-TEMPLATE-NOT-FOUND-001", "job.template_id does not reference an available template"))
		return false
	}
	if item.Target != "" {
		target := strings.TrimSpace(request.Job.Target)
		if target == "" {
			request.Job.Target = item.Target
		} else if target != item.Target {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-TEMPLATE-TARGET-001", "job.target must match the referenced template target"))
			return false
		}
	}
	if item.CommandType != "" {
		commandType := strings.TrimSpace(request.Job.CommandType)
		if commandType == "" {
			request.Job.CommandType = item.CommandType
		} else if commandType != item.CommandType {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-TEMPLATE-COMMAND-001", "job.command_type must match the referenced template command_type"))
			return false
		}
	}
	request.Job.TemplateID = item.ID
	request.Job.Input = mergeMaps(item.InputDefaults, request.Job.Input)
	request.Job.Options = mergeMaps(item.OptionsDefaults, request.Job.Options)
	if item.Sensitive {
		request.Job.Options = mergeMaps(request.Job.Options, map[string]any{"cache_policy": "none"})
	}
	return true
}

func mergeMaps(defaults map[string]any, overrides map[string]any) map[string]any {
	if len(defaults) == 0 && len(overrides) == 0 {
		return nil
	}
	output := cloneMap(defaults)
	if output == nil {
		output = map[string]any{}
	}
	for key, value := range overrides {
		output[key] = normalizeJSONValue(value)
	}
	return output
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = normalizeJSONValue(value)
	}
	return output
}

func normalizeJSONValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case map[string]any:
		return cloneMap(typed)
	case []any:
		output := make([]any, 0, len(typed))
		for _, item := range typed {
			output = append(output, normalizeJSONValue(item))
		}
		return output
	default:
		return value
	}
}

func validateExecutableJobPayload(request createJobRequest) error {
	return jobcore.ValidatePayload(jobcoreClient(request.Client), jobcoreSpec(request.Job))
}

func queueMetricStates(stats executor.Stats) []string {
	states := map[string]struct{}{"queued": {}}
	for state := range stats.DepthByState {
		states[state] = struct{}{}
	}
	for state := range stats.OldestAgeByState {
		states[state] = struct{}{}
	}
	items := make([]string, 0, len(states))
	for state := range states {
		items = append(items, state)
	}
	sort.Strings(items)
	if stats.QueueName == "" {
		stats.QueueName = "jobs"
	}
	return items
}

func webhookMetricStates(stats webhooks.Stats) []string {
	states := map[string]struct{}{
		string(webhooks.StatusPending):        {},
		string(webhooks.StatusRetryScheduled): {},
		string(webhooks.StatusLeased):         {},
		string(webhooks.StatusDelivered):      {},
		string(webhooks.StatusDeadLettered):   {},
	}
	for state := range stats.DepthByState {
		states[state] = struct{}{}
	}
	for state := range stats.OldestAgeByState {
		states[state] = struct{}{}
	}
	items := make([]string, 0, len(states))
	for state := range states {
		items = append(items, state)
	}
	sort.Strings(items)
	return items
}

func _canonicalWebhookReplayPayload(request webhookReplayRequest) string {
	payload := map[string]any{
		"api_version": strings.TrimSpace(request.APIVersion),
		"webhook_id":  strings.TrimSpace(request.WebhookID),
		"delivery_id": strings.TrimSpace(request.DeliveryID),
		"reason":      strings.TrimSpace(request.Reason),
		"metadata":    request.Metadata,
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func webhookReplayResourceID(request webhookReplayRequest) string {
	resourceID := firstNonEmpty(request.DeliveryID, request.WebhookID)
	if resourceID == "" {
		return "webhook_replay:" + hashString(_canonicalWebhookReplayPayload(request))[:16]
	}
	return resourceID
}

func replayHTTPStatus(record idempotency.Record, fallback int) int {
	if record.HTTPStatus != 0 {
		return record.HTTPStatus
	}
	return fallback
}

func sortJobs(jobs []jobstore.Job, sortParam string) {
	descending := sortParam == "" || sortParam == "-created_at"
	sort.SliceStable(jobs, func(left, right int) bool {
		if descending {
			return jobs[left].CreatedAt.After(jobs[right].CreatedAt)
		}
		return jobs[left].CreatedAt.Before(jobs[right].CreatedAt)
	})
}

func jobsAfterCursor(jobs []jobstore.Job, cursor string) []jobstore.Job {
	for index, job := range jobs {
		if job.ID == cursor {
			return jobs[index+1:]
		}
	}
	return jobs
}

func collectionAfterCursor(items []map[string]any, cursor string) []map[string]any {
	if cursor == "" {
		return cloneCollection(items)
	}
	for index, item := range items {
		if collectionItemCursor(item) == cursor {
			return cloneCollection(items[index+1:])
		}
	}
	return cloneCollection(items)
}

func collectionNextCursor(items []map[string]any, limit int) *string {
	if len(items) <= limit || limit <= 0 {
		return nil
	}
	cursor := collectionItemCursor(items[limit-1])
	if cursor == "" {
		return nil
	}
	return &cursor
}

func collectionItemCursor(item map[string]any) string {
	for _, key := range []string{"id", "key", "event_id"} {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneCollection(items []map[string]any) []map[string]any {
	output := make([]map[string]any, 0, len(items))
	for _, item := range items {
		output = append(output, cloneMap(item))
	}
	return output
}

func (s *Server) loadAuthorizedJob(w http.ResponseWriter, r *http.Request, id string, action string) (jobstore.Job, bool) {
	if !s.authorizeGatewayAction(w, r, action) {
		return jobstore.Job{}, false
	}

	tenantID, appID := requestScope(r)
	if scopedStore, ok := s.jobs.(jobstore.ScopedStore); ok {
		job, found, err := scopedStore.GetScoped(r.Context(), id, tenantID, appID)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load job"))
			return jobstore.Job{}, false
		}
		if !found {
			s.writeJobNotFound(w, r)
			return jobstore.Job{}, false
		}
		return job, true
	}

	job, found, err := s.jobs.Get(r.Context(), id)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load job"))
		return jobstore.Job{}, false
	}
	if !found || job.TenantID != tenantID || job.AppID != appID {
		s.writeJobNotFound(w, r)
		return jobstore.Job{}, false
	}
	return job, true
}

func (s *Server) authorizeJobAccess(w http.ResponseWriter, r *http.Request, job jobstore.Job, action string) bool {
	if !s.authorizeGatewayAction(w, r, action) {
		return false
	}

	tenantID, appID := requestScope(r)
	if job.TenantID != tenantID || job.AppID != appID {
		s.writeJobNotFound(w, r)
		return false
	}
	return true
}

func (s *Server) authorizeGatewayAction(w http.ResponseWriter, r *http.Request, action string) bool {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		s.writeError(w, r, http.StatusUnauthorized, authError("UBAG-AUTH-MISSING-001", "missing authenticated principal"))
		return false
	}

	role := principal.Role
	allowed := false
	switch role {
	case "developer":
		allowed = action == "job:create" || action == "job:read" || action == "job:cancel" || action == "job:retry" || action == "artifact:write" || action == "artifact:delete" || action == "webhook:configure" || action == "browser:read" || action == "concurrency:read"
	case "operator":
		allowed = action == "job:create" || action == "job:read" || action == "job:cancel" || action == "job:retry" || action == "artifact:write" || action == "artifact:delete" || action == "device:enroll" || action == "device:revoke" || action == "webhook:configure" || action == "webhook:replay" || action == "audit:read" || action == "alerts:read" || action == "alerts:manage" || action == "browser:read" || action == "concurrency:read"
	case "admin":
		allowed = action == "job:create" || action == "job:read" || action == "job:cancel" || action == "job:retry" || action == "artifact:write" || action == "artifact:delete" || action == "device:enroll" || action == "device:revoke" || action == "secret:rotate" || action == "webhook:configure" || action == "webhook:replay" || action == "audit:read" || action == "rate_limit:manage" || action == "role:manage" || action == "data:export" || action == "alerts:read" || action == "alerts:manage" || action == "browser:read" || action == "concurrency:read" || action == "region:manage"
	case "superadmin":
		allowed = true
	case "service":
		allowed = action == "job:create" || action == "job:read" || action == "job:cancel" || action == "job:retry" || action == "artifact:write" || action == "artifact:delete" || action == "webhook:replay"
	case "viewer":
		allowed = action == "job:read"
	default:
		allowed = false
	}
	if !allowed {
		s.emitAuthorizationAudit(r, principal, action, "deny")
		s.writeError(w, r, http.StatusForbidden, authzError("UBAG-AUTHZ-ROLE-DENIED-001", "actor role is not allowed to perform this action"))
		return false
	}

	// MFA gate: certain sensitive actions require a verified MFA session.
	// Only enforced when MFA is enabled (s.mfaSvc != nil) AND the request is
	// authenticated via an SSO session (static API keys are exempt).
	if s.mfaSvc != nil && principal.SessionBased {
		if action == "role:manage" || action == "data:export" || action == "region:manage" {
			if !principal.MFAVerified {
				s.emitAuthorizationAudit(r, principal, action, "deny-mfa-required")
				s.writeError(w, r, http.StatusForbidden, authzError("UBAG-AUTHZ-MFA-REQUIRED-001", "this action requires MFA verification"))
				return false
			}
		}
	}

	// ABAC: evaluate CEL policy bundle after RBAC passes.
	if s.abacEnforcer != nil {
		abacPrincipal := abac.Principal{
			TenantID: principal.TenantID,
			AppID:    principal.AppID,
			Role:     principal.Role,
			Subject:  principal.Subject,
		}
		ok, err := s.abacEnforcer.Allow(abacPrincipal, "gateway", action)
		if err != nil || !ok {
			s.emitAuthorizationAudit(r, principal, action, "deny-abac")
			s.writeError(w, r, http.StatusForbidden, authzError("UBAG-AUTHZ-POLICY-DENIED-001", "request denied by access policy"))
			return false
		}
	}

	s.emitAuthorizationAudit(r, principal, action, "allow")
	return true
}

// emitAuthorizationAudit appends a best-effort, Merkle-chained audit record for
// an authorization decision. It is nil-safe and never blocks the request: any
// store error is intentionally ignored so auditing cannot fail the gateway.
func (s *Server) emitAuthorizationAudit(r *http.Request, principal authenticatedPrincipal, action, outcome string) {
	if s == nil || s.audit == nil {
		return
	}
	actor := principal.Subject
	if actor == "" {
		actor = principal.Role
	}
	_, _ = s.audit.Append(r.Context(), audit.Record{
		TenantID:   principal.TenantID,
		AppID:      principal.AppID,
		Actor:      actor,
		Action:     "authorize:" + action,
		Resource:   r.URL.Path,
		Outcome:    outcome,
		OccurredAt: time.Now(),
		Attributes: map[string]any{"role": principal.Role, "method": r.Method},
	})
}

func targetCatalog() []map[string]any {
	return []map[string]any{
		{"key": "mock", "adapter_key": "mock", "display_name": "Mock Target", "safe_mode": true, "manual_login_required": false},
		{"key": "deepseek_web", "adapter_key": "deepseek_web", "display_name": "DeepSeek Web", "safe_mode": true, "manual_login_required": true},
		{"key": "chatgpt_web", "adapter_key": "chatgpt_web", "display_name": "ChatGPT Web", "safe_mode": true, "manual_login_required": true},
		{"key": "claude_web", "adapter_key": "claude_web", "display_name": "Claude Web", "safe_mode": true, "manual_login_required": true},
		{"key": "gemini_web", "adapter_key": "gemini_web", "display_name": "Gemini Web", "safe_mode": true, "manual_login_required": true},
		{"key": "mistral_lechat", "adapter_key": "mistral_lechat", "display_name": "Mistral Le Chat", "safe_mode": true, "manual_login_required": true},
		{"key": "perplexity_web", "adapter_key": "perplexity_web", "display_name": "Perplexity Web", "safe_mode": true, "manual_login_required": true},
		{"key": "generic_chat", "adapter_key": "generic_chat", "display_name": "Generic Chat", "safe_mode": true, "manual_login_required": true},
		{"key": "generic_form", "adapter_key": "generic_form", "display_name": "Generic Form", "safe_mode": true, "manual_login_required": true},
	}
}

func adapterCatalog() []map[string]any {
	return []map[string]any{
		{"key": "mock", "kind": "mock", "stage": "v0", "capabilities": []string{"submit", "stream", "extract", "normalize"}},
		{"key": "deepseek_web", "kind": "browser", "stage": "v1", "capabilities": []string{"manual_login", "submit", "stream", "extract", "normalize"}},
		{"key": "chatgpt_web", "kind": "browser", "stage": "v1", "capabilities": []string{"manual_login", "submit", "stream", "extract", "normalize"}},
		{"key": "claude_web", "kind": "browser", "stage": "v1", "capabilities": []string{"manual_login", "submit", "stream", "extract", "normalize"}},
		{"key": "gemini_web", "kind": "browser", "stage": "v1", "capabilities": []string{"manual_login", "submit", "stream", "extract", "normalize"}},
		{"key": "mistral_lechat", "kind": "browser", "stage": "v1", "capabilities": []string{"manual_login", "submit", "stream", "extract", "normalize"}},
		{"key": "perplexity_web", "kind": "browser", "stage": "v1", "capabilities": []string{"manual_login", "submit", "stream", "extract", "normalize"}},
		{"key": "generic_chat", "kind": "browser", "stage": "v0", "capabilities": []string{"manual_login", "submit", "extract", "normalize"}},
		{"key": "generic_form", "kind": "browser", "stage": "v0", "capabilities": []string{"manual_login", "submit", "extract", "normalize"}},
	}
}

func promLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return value
}

func websocketAcceptKey(key string) string {
	const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func websocketTextFrame(payload []byte) []byte {
	frame := []byte{0x81}
	length := len(payload)
	switch {
	case length < 126:
		frame = append(frame, byte(length))
	case length <= 65535:
		frame = append(frame, 126, byte(length>>8), byte(length))
	default:
		frame = append(frame, 127, byte(length>>56), byte(length>>48), byte(length>>40), byte(length>>32), byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	return append(frame, payload...)
}

func constantTimeEqual(actual, expected string) bool {
	actualHash := sha256.Sum256([]byte(actual))
	expectedHash := sha256.Sum256([]byte(expected))
	sameLength := subtle.ConstantTimeEq(int32(len(actual)), int32(len(expected)))
	sameValue := subtle.ConstantTimeCompare(actualHash[:], expectedHash[:])
	return sameLength&sameValue == 1
}

func validBearerToken(header string, expectedSecret string) bool {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	return constantTimeEqual(parts[1], expectedSecret)
}

// bearerToken extracts the token value from an "Authorization: Bearer <token>"
// header. Returns empty string if the header is absent or malformed.
func bearerToken(header string) string {
	parts := strings.Fields(header)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

type metricSnapshot struct {
	route       string
	method      string
	statusClass string
	outcome     string
	count       int
	durationSum float64
}

type traceContextKey struct{}
type principalContextKey struct{}

type authenticatedPrincipal struct {
	Role         string
	TenantID     string
	AppID        string
	Subject      string
	MFAVerified  bool // true when the principal's session has completed MFA verification
	SessionBased bool // true when authenticated via an SSO session token (not a static API key)
}

// mfaSessionSet is a concurrency-safe in-memory set of session IDs that have
// been verified via MFA (POST /v1/mfa/verify).
type mfaSessionSet struct {
	mu  sync.Mutex
	set map[string]struct{}
}

func (s *mfaSessionSet) Add(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set == nil {
		s.set = make(map[string]struct{})
	}
	s.set[sessionID] = struct{}{}
}

func (s *mfaSessionSet) Contains(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.set[sessionID]
	return ok
}

func (s *Server) withMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		s.recordMetric(routePattern(r.URL.Path), r.Method, status, time.Since(start))
	})
}

func (s *Server) recordMetric(route, method string, status int, duration time.Duration) {
	statusClass := fmt.Sprintf("%dxx", status/100)
	outcome := "success"
	if status >= 400 {
		outcome = "error"
	}
	key := strings.Join([]string{route, method, statusClass, outcome}, "\x00")

	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	s.metrics.requests[key]++
	s.metrics.durationSum[key] += duration.Seconds()
}

func (s *Server) incrementSSEConnections() {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	s.metrics.sseCurrent++
}

func (s *Server) decrementSSEConnections() {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	if s.metrics.sseCurrent > 0 {
		s.metrics.sseCurrent--
	}
}

func (s *Server) currentSSEConnections() int {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	return s.metrics.sseCurrent
}

func (s *Server) metricsSnapshot() []metricSnapshot {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()

	snapshots := make([]metricSnapshot, 0, len(s.metrics.requests))
	for key, count := range s.metrics.requests {
		parts := strings.Split(key, "\x00")
		if len(parts) != 4 {
			continue
		}
		snapshots = append(snapshots, metricSnapshot{
			route:       parts[0],
			method:      parts[1],
			statusClass: parts[2],
			outcome:     parts[3],
			count:       count,
			durationSum: s.metrics.durationSum[key],
		})
	}
	sort.Slice(snapshots, func(left, right int) bool {
		return strings.Join([]string{snapshots[left].route, snapshots[left].method, snapshots[left].statusClass}, "\x00") <
			strings.Join([]string{snapshots[right].route, snapshots[right].method, snapshots[right].statusClass}, "\x00")
	})
	return snapshots
}

func routePattern(path string) string {
	segments := splitRouteTail(path, "/")
	if len(segments) >= 3 && segments[0] == "v1" && segments[1] == "jobs" {
		if len(segments) == 3 {
			return "/v1/jobs/{job_id}"
		}
		if len(segments) == 4 && segments[3] == "artifacts" {
			return "/v1/jobs/{job_id}/artifacts"
		}
		if len(segments) == 5 && segments[3] == "artifacts" {
			return "/v1/jobs/{job_id}/artifacts/{key}"
		}
		if len(segments) == 4 && (segments[3] == "events" || segments[3] == "cancel" || segments[3] == "retry") {
			return "/v1/jobs/{job_id}/" + segments[3]
		}
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "sse" && segments[2] == "jobs" {
		return "/v1/sse/jobs/{job_id}"
	}
	if strings.HasPrefix(path, "/v1/sse/jobs/") {
		return "/v1/sse/jobs/{job_id}"
	}
	if len(segments) == 5 && segments[0] == "v1" && segments[1] == "scim" && segments[2] == "v2" {
		switch segments[3] {
		case "Users":
			return "/v1/scim/v2/Users/{id}"
		case "Groups":
			return "/v1/scim/v2/Groups/{id}"
		}
	}
	if len(segments) >= 3 && segments[0] == "v1" && segments[1] == "workflows" {
		if len(segments) == 3 && segments[2] == "runs" {
			return "/v1/workflows/runs"
		}
		if len(segments) == 3 {
			return "/v1/workflows/{id}"
		}
		if len(segments) == 4 && segments[2] == "runs" {
			return "/v1/workflows/runs/{id}"
		}
		if len(segments) == 4 && segments[3] == "runs" {
			return "/v1/workflows/{id}/runs"
		}
	}
	return path
}

func (s *Server) withTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := firstNonEmpty(r.Header.Get("X-Request-Id"), generatedTraceID())
		w.Header().Set("X-Request-Id", traceID)
		w.Header().Set("Ubag-Trace-Id", traceID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), traceContextKey{}, traceID)))
	})
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresAuth(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		if validBearerToken(r.Header.Get("Authorization"), s.appSecret) {
			principal := authenticatedPrincipal{
				Role:     s.actorRole,
				TenantID: s.tenantID,
				AppID:    s.appID,
			}
			principal = s.applyJITElevation(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
			return
		}

		// App JWT: validate RS256 Bearer token.
		if s.appJWTPublicKey != nil {
			if bearer := bearerToken(r.Header.Get("Authorization")); bearer != "" {
				if claims, err := appjwt.Verify(bearer, s.appJWTPublicKey); err == nil {
					principal := authenticatedPrincipal{
						Role:     claims.Role,
						TenantID: claims.TenantID,
						AppID:    claims.AppID,
					}
					principal = s.applyJITElevation(r.Context(), principal)
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
					return
				}
			}
		}

		// PAT: resolve a personal access token (ubag_pat_... Bearer).
		if s.patStore != nil {
			if bearer := bearerToken(r.Header.Get("Authorization")); pat.IsValidFormat(bearer) {
				token, ok, err := s.patStore.Resolve(r.Context(), bearer, time.Now())
				if err == nil && ok {
					principal := authenticatedPrincipal{
						Role:     token.Role,
						TenantID: token.TenantID,
						AppID:    token.AppID,
					}
					principal = s.applyJITElevation(r.Context(), principal)
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
					return
				}
			}
		}

		// Additive: resolve a server-side SSO session from a cookie or bearer
		// token when the static app-secret did not match. Sessions never replace
		// the app-secret path; they extend it.
		if s.sessions != nil {
			if token := sessionTokenFromRequest(r); token != "" {
				sess, ok, err := s.sessions.Resolve(r.Context(), token, time.Now())
				if err == nil && ok {
					principal := authenticatedPrincipal{
						Role:         sess.Role,
						TenantID:     sess.TenantID,
						AppID:        sess.AppID,
						Subject:      sess.Subject,
						MFAVerified:  s.mfaSessions.Contains(token),
						SessionBased: true,
					}
					principal = s.applyJITElevation(r.Context(), principal)
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
					return
				}
			}
		}

		s.writeError(w, r, http.StatusUnauthorized, authError("UBAG-AUTH-MISSING-001", "missing or invalid credentials"))
	})
}

// applyJITElevation checks whether there is an active JIT elevation grant for
// the principal and, if so, returns a copy with an elevated Role. When
// s.jitAdmin is nil (feature not enabled) or p.Subject is empty (bearer-secret
// or machine-API-key auth that carries no Subject), the principal is returned
// unchanged. Requiring a non-empty Subject prevents a grant for actor="service"
// from escalating ALL bearer-secret requests (privilege-escalation guard).
func (s *Server) applyJITElevation(ctx context.Context, p authenticatedPrincipal) authenticatedPrincipal {
	if s.jitAdmin == nil || p.Subject == "" {
		return p
	}
	elevated := jitadmin.ElevatedRole(ctx, s.jitAdmin, p.Subject, p.TenantID, p.Role, time.Now())
	if elevated != p.Role {
		p.Role = elevated
	}
	return p
}

// sessionCookieName is the cookie that carries an opaque SSO session token.
const sessionCookieName = "ubag_session"

// sessionTokenFromRequest extracts a session token from the session cookie or,
// failing that, the bearer Authorization header (used when the bearer value is
// not the app secret).
func sessionTokenFromRequest(r *http.Request) string {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

func (s *Server) withAPIVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerVersion := strings.TrimSpace(r.Header.Get(headerAPIVersion))
		if headerVersion != "" && !s.isSupportedAPIVersion(headerVersion) {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-API-VERSION-UNSUPPORTED-001", "requested API version is not supported"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requiresAuth(path string) bool {
	switch path {
	case "/v1/health", "/v1/ready", "/v1/version", "/v1/metrics":
		return false
	// The authorization-code flow endpoints are browser-facing (no bearer token).
	// The IdP redirects the user's browser to /authorize and then to /callback,
	// so these two paths must be reachable without a pre-existing credential.
	case "/v1/sso/oidc/authorize", "/v1/sso/oidc/callback":
		return false
	default:
		return strings.HasPrefix(path, "/v1/")
	}
}

func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.writeError(w, r, http.StatusInternalServerError, internalError("gateway recovered from an unexpected error"))
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func traceIDFromContext(ctx context.Context) string {
	if traceID, ok := ctx.Value(traceContextKey{}).(string); ok && traceID != "" {
		return traceID
	}

	return generatedTraceID()
}

func principalFromContext(ctx context.Context) (authenticatedPrincipal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(authenticatedPrincipal)
	return principal, ok
}

func parseEnvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return fallback
}

func generatedTraceID() string {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return fmt.Sprintf("trace_%d", time.Now().UTC().UnixNano())
	}

	return "trace_" + hex.EncodeToString(buffer[:])
}

// ---------------------------------------------------------------------------
// Artifact handlers: /v1/jobs/{id}/artifacts[/{key}]
// ---------------------------------------------------------------------------

const maxArtifactBodyBytes = 32 << 20 // 32 MiB upload limit

type artifactRecordResponse struct {
	JobID       string    `json:"job_id"`
	Key         string    `json:"key"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	Checksum    string    `json:"checksum,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Server) listJobArtifacts(w http.ResponseWriter, r *http.Request, jobID string) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		s.writeNotFound(w, r)
		return
	}

	job, ok := s.loadAuthorizedJob(w, r, jobID, "job:read")
	if !ok {
		return
	}

	recs, err := s.artifactSt.ListArtifacts(r.Context(), job.ID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list artifacts"))
		return
	}
	if recs == nil {
		recs = []artifacts.ArtifactRecord{}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"api_version": s.apiVersion,
		"job_id":      job.ID,
		"kind":        "artifacts",
		"data":        artifactRecordsToResponses(recs),
		"trace_id":    traceIDFromContext(r.Context()),
	})
}

func (s *Server) putJobArtifact(w http.ResponseWriter, r *http.Request, jobID, key string) {
	jobID = strings.TrimSpace(jobID)
	key = strings.TrimSpace(key)
	if jobID == "" || key == "" {
		s.writeNotFound(w, r)
		return
	}
	if !validArtifactKey(key) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-ARTIFACT-KEY-001", "artifact key must be a single non-empty path segment"))
		return
	}

	job, ok := s.loadAuthorizedJob(w, r, jobID, "artifact:write")
	if !ok {
		return
	}
	if r.ContentLength > maxArtifactBodyBytes {
		s.writeError(w, r, http.StatusRequestEntityTooLarge, validationError("UBAG-VALIDATION-BODY-TOO-LARGE-001", "artifact body exceeds 32 MiB gateway limit"))
		return
	}
	if r.ContentLength < 0 {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-LENGTH-001", "artifact upload requires Content-Length"))
		return
	}

	contentType := safeArtifactContentType(r.Header.Get("Content-Type"))
	body := http.MaxBytesReader(w, r.Body, maxArtifactBodyBytes)
	payload, err := io.ReadAll(body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			s.writeError(w, r, http.StatusRequestEntityTooLarge, validationError("UBAG-VALIDATION-BODY-TOO-LARGE-001", "artifact body exceeds 32 MiB gateway limit"))
			return
		}
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-READ-001", "artifact body could not be read"))
		return
	}
	if int64(len(payload)) != r.ContentLength {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-LENGTH-001", "artifact Content-Length must match the uploaded body length"))
		return
	}
	requestHash := canonicalArtifactMutationHash(r.Method, r.URL.Path, "put_artifact", job.ID, key, contentType, payload)
	mutation, ok := s.reserveArtifactMutation(w, r, "put_artifact", job.ID, key, requestHash)
	if !ok {
		return
	}
	if mutation.replay {
		s.replayPutArtifact(w, r, job.ID, key)
		return
	}
	sizeBytes := int64(len(payload))

	rec, err := s.artifactSt.PutArtifact(r.Context(), job.ID, key, contentType, bytes.NewReader(payload), sizeBytes)
	if err != nil {
		if artifacts.IsInvalid(err) {
			_ = s.idempotency.Release(r.Context(), mutation.scope)
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-ARTIFACT-KEY-001", "artifact key must be a single non-empty path segment"))
			return
		}
		_ = s.idempotency.Release(r.Context(), mutation.scope)
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to store artifact"))
		return
	}
	if err := s.idempotency.Complete(r.Context(), mutation.scope, artifactResourceID(job.ID, key), http.StatusCreated); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to complete idempotency record"))
		return
	}

	s.artifactCaptures.Add(1) // ubag_artifact_captures_total
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"api_version": s.apiVersion,
		"artifact":    artifactRecordToResponse(rec),
		"trace_id":    traceIDFromContext(r.Context()),
	})
}

func (s *Server) getJobArtifact(w http.ResponseWriter, r *http.Request, jobID, key string) {
	jobID = strings.TrimSpace(jobID)
	key = strings.TrimSpace(key)
	if jobID == "" || key == "" {
		s.writeNotFound(w, r)
		return
	}
	if !validArtifactKey(key) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-ARTIFACT-KEY-001", "artifact key must be a single non-empty path segment"))
		return
	}

	job, ok := s.loadAuthorizedJob(w, r, jobID, "job:read")
	if !ok {
		return
	}

	rc, rec, err := s.artifactSt.GetArtifact(r.Context(), job.ID, key)
	if err != nil {
		if artifacts.IsNotFound(err) {
			s.writeNotFound(w, r)
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to retrieve artifact"))
		return
	}
	defer rc.Close()

	ct := rec.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment")
	if rec.SizeBytes > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", rec.SizeBytes))
	}
	if rec.Checksum != "" {
		w.Header().Set("Ubag-Artifact-Checksum", rec.Checksum)
		w.Header().Set("ETag", fmt.Sprintf("%q", rec.Checksum))
	}
	w.Header().Set("Cache-Control", "private, immutable")
	if !rec.CreatedAt.IsZero() {
		w.Header().Set("Last-Modified", rec.CreatedAt.UTC().Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (s *Server) deleteJobArtifact(w http.ResponseWriter, r *http.Request, jobID, key string) {
	jobID = strings.TrimSpace(jobID)
	key = strings.TrimSpace(key)
	if jobID == "" || key == "" {
		s.writeNotFound(w, r)
		return
	}
	if !validArtifactKey(key) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-ARTIFACT-KEY-001", "artifact key must be a single non-empty path segment"))
		return
	}

	job, ok := s.loadAuthorizedJob(w, r, jobID, "artifact:delete")
	if !ok {
		return
	}
	requestHash := canonicalArtifactMutationHash(r.Method, r.URL.Path, "delete_artifact", job.ID, key, "", nil)
	mutation, ok := s.reserveArtifactMutation(w, r, "delete_artifact", job.ID, key, requestHash)
	if !ok {
		return
	}
	if mutation.replay {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := s.artifactSt.DeleteArtifact(r.Context(), job.ID, key); err != nil {
		if artifacts.IsNotFound(err) {
			_ = s.idempotency.Release(r.Context(), mutation.scope)
			s.writeNotFound(w, r)
			return
		}
		_ = s.idempotency.Release(r.Context(), mutation.scope)
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to delete artifact"))
		return
	}
	if err := s.idempotency.Complete(r.Context(), mutation.scope, artifactResourceID(job.ID, key), http.StatusNoContent); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to complete idempotency record"))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func artifactRecordsToResponses(records []artifacts.ArtifactRecord) []artifactRecordResponse {
	result := make([]artifactRecordResponse, 0, len(records))
	for _, rec := range records {
		result = append(result, artifactRecordToResponse(rec))
	}
	return result
}

func artifactRecordToResponse(rec artifacts.ArtifactRecord) artifactRecordResponse {
	return artifactRecordResponse{
		JobID:       rec.JobID,
		Key:         rec.Key,
		ContentType: rec.ContentType,
		SizeBytes:   rec.SizeBytes,
		Checksum:    rec.Checksum,
		CreatedAt:   rec.CreatedAt,
	}
}

func validArtifactKey(key string) bool {
	key = strings.TrimSpace(key)
	return key != "" && key != "." && key != ".." && !strings.ContainsAny(key, "/\\?\x00") && !strings.Contains(key, "%")
}

func safeArtifactContentType(raw string) string {
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil || mediaType == "" {
		return "application/octet-stream"
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "application/json", "application/pdf", "application/octet-stream", "text/plain",
		"image/gif", "image/jpeg", "image/png", "image/webp":
		return mediaType
	default:
		return "application/octet-stream"
	}
}

// ---------------------------------------------------------------------------
// MFA handlers: POST /v1/mfa/enroll, POST /v1/mfa/verify
// ---------------------------------------------------------------------------

// handleMFAEnroll enrolls the current user in TOTP-based MFA.
// RBAC: job:create (any authenticated user may enroll themselves).
// Returns the base32 secret, an otpauth:// URI for QR scanning, and one-time
// recovery codes. When MFA is not configured (s.mfaSvc == nil) returns 501.
func (s *Server) handleMFAEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}
	if s.mfaSvc == nil {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-MFA-NOT-ENABLED-001", "MFA is not enabled on this gateway"))
		return
	}

	var body struct {
		Issuer string `json:"issuer"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, s.maxBody)).Decode(&body); err != nil && err != io.EOF {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-001", "invalid request body"))
		return
	}

	principal, _ := principalFromContext(r.Context())
	issuer := strings.TrimSpace(body.Issuer)
	if issuer == "" {
		issuer = "UBAG"
	}

	result, err := s.mfaSvc.Enroll(r.Context(), mfa.EnrollRequest{
		TenantID: principal.TenantID,
		UserID:   principal.Subject,
		Issuer:   issuer,
	})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to enroll MFA"))
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"secret":         result.Secret,
		"otpauth_uri":    result.OTPAuthURI,
		"recovery_codes": result.RecoveryCodes,
	})
}

// handleMFAVerify verifies a TOTP code or recovery code for the current user.
// On success it marks the current session as MFA-verified. RBAC: job:create.
func (s *Server) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}
	if s.mfaSvc == nil {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-MFA-NOT-ENABLED-001", "MFA is not enabled on this gateway"))
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, s.maxBody)).Decode(&body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-001", "invalid request body"))
		return
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MFA-CODE-001", "code is required"))
		return
	}

	principal, _ := principalFromContext(r.Context())
	ok, err := s.mfaSvc.Verify(r.Context(), principal.TenantID, principal.Subject, code)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("MFA verification error"))
		return
	}
	if !ok {
		s.writeError(w, r, http.StatusUnauthorized, authError("UBAG-MFA-INVALID-CODE-001", "MFA code is invalid or expired"))
		return
	}

	// Mark the session token as MFA-verified for subsequent requests.
	if token := sessionTokenFromRequest(r); token != "" {
		s.mfaSessions.Add(token)
	}

	w.Header().Set("X-MFA-Verified", "true")
	s.writeJSON(w, http.StatusOK, map[string]any{"verified": true})
}

// elevationRequest is the body accepted by POST /v1/admin/elevation.
type elevationRequest struct {
	Role       string `json:"role"`
	TTLSeconds int64  `json:"ttl_seconds"`
	Reason     string `json:"reason"`
}

// handleRequestElevation implements POST /v1/admin/elevation.
// RBAC: job:create (any authenticated user may request elevation).
// Body: {"role": "admin", "ttl_seconds": 3600, "reason": "incident response"}
// Returns 201 with the new Grant on success.
func (s *Server) handleRequestElevation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if s.jitAdmin == nil {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-JITADMIN-NOT-ENABLED-001", "JIT admin elevation is not enabled on this gateway"))
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}

	var body elevationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, s.maxBody)).Decode(&body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-001", "invalid request body"))
		return
	}
	if strings.TrimSpace(body.Role) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-JITADMIN-ROLE-001", "role is required"))
		return
	}
	if body.TTLSeconds <= 0 {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-JITADMIN-TTL-001", "ttl_seconds must be a positive integer"))
		return
	}

	principal, _ := principalFromContext(r.Context())
	if principal.Subject == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-JIT-SUBJECT-REQUIRED-001", "JIT elevation requires a session-based principal with a non-empty subject; bearer-secret and machine API keys do not support elevation"))
		return
	}
	actor := principal.Subject
	tenantID, appID := requestScope(r)
	now := time.Now()
	ttl := time.Duration(body.TTLSeconds) * time.Second

	grant, err := s.jitAdmin.Create(r.Context(), jitadmin.Grant{
		Actor:     actor,
		TenantID:  tenantID,
		AppID:     appID,
		Role:      body.Role,
		Reason:    body.Reason,
		TTL:       ttl,
		CreatedAt: now,
	})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to create elevation grant"))
		return
	}

	_, _ = s.audit.Append(r.Context(), audit.Record{
		TenantID:   tenantID,
		AppID:      appID,
		Actor:      actor,
		Action:     "jitadmin:request",
		Resource:   "/v1/admin/elevation",
		Outcome:    "created",
		OccurredAt: now,
		Attributes: map[string]any{
			"grant_id":    grant.ID,
			"role":        grant.Role,
			"ttl_seconds": body.TTLSeconds,
			"reason":      grant.Reason,
		},
	})

	s.writeJSON(w, http.StatusCreated, grant)
}

// handleApproveElevation implements POST /v1/admin/elevation/{id}/approve.
// RBAC: role:manage (admin or superadmin only).
// Returns 200 with the updated Grant on success.
func (s *Server) handleApproveElevation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		return
	}
	if s.jitAdmin == nil {
		s.writeError(w, r, http.StatusNotImplemented, validationError("UBAG-JITADMIN-NOT-ENABLED-001", "JIT admin elevation is not enabled on this gateway"))
		return
	}
	if !s.authorizeGatewayAction(w, r, "role:manage") {
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-JITADMIN-ID-001", "grant id is required"))
		return
	}

	principal, _ := principalFromContext(r.Context())
	approver := principal.Subject
	if approver == "" {
		approver = principal.Role
	}
	tenantID, appID := requestScope(r)
	now := time.Now()

	grant, err := s.jitAdmin.Approve(r.Context(), id, approver, now)
	if err != nil {
		if errors.Is(err, jitadmin.ErrGrantNotFound) {
			s.writeError(w, r, http.StatusNotFound, validationError("UBAG-JITADMIN-NOT-FOUND-001", "elevation grant not found"))
			return
		}
		if errors.Is(err, jitadmin.ErrGrantExpired) {
			s.writeError(w, r, http.StatusConflict, validationError("UBAG-JITADMIN-EXPIRED-001", "elevation grant has expired and cannot be approved"))
			return
		}
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to approve elevation grant"))
		return
	}

	_, _ = s.audit.Append(r.Context(), audit.Record{
		TenantID:   tenantID,
		AppID:      appID,
		Actor:      approver,
		Action:     "jitadmin:approve",
		Resource:   "/v1/admin/elevation/" + id + "/approve",
		Outcome:    "approved",
		OccurredAt: now,
		Attributes: map[string]any{
			"grant_id":   grant.ID,
			"grant_role": grant.Role,
			"grantee":    grant.Actor,
		},
	})

	s.writeJSON(w, http.StatusOK, grant)
}
