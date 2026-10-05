package serve

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/ubag/ubag/apps/gateway/internal/abac"
	"github.com/ubag/ubag/apps/gateway/internal/alerts"
	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/conversations"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/grpcapi"
	"github.com/ubag/ubag/apps/gateway/internal/httpapi"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	"github.com/ubag/ubag/apps/gateway/internal/jitadmin"
	"github.com/ubag/ubag/apps/gateway/internal/jobcore"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/mfa"
	"github.com/ubag/ubag/apps/gateway/internal/obs"
	"github.com/ubag/ubag/apps/gateway/internal/pat"
	"github.com/ubag/ubag/apps/gateway/internal/profile"
	"github.com/ubag/ubag/apps/gateway/internal/ratelimit"
	"github.com/ubag/ubag/apps/gateway/internal/region"
	"github.com/ubag/ubag/apps/gateway/internal/resilience"
	"github.com/ubag/ubag/apps/gateway/internal/responsecache"
	"github.com/ubag/ubag/apps/gateway/internal/scim"
	"github.com/ubag/ubag/apps/gateway/internal/session"
	"github.com/ubag/ubag/apps/gateway/internal/siem"
	"github.com/ubag/ubag/apps/gateway/internal/sqlitestore"
	"github.com/ubag/ubag/apps/gateway/internal/sso"
	"github.com/ubag/ubag/apps/gateway/internal/storekit"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	voice "github.com/ubag/ubag/apps/gateway/internal/voice"
	"github.com/ubag/ubag/apps/gateway/internal/webhooks"
	"github.com/ubag/ubag/apps/gateway/internal/workflow"
	ubagv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	_ "modernc.org/sqlite"
)

// defaultSQLiteDSN enables WAL mode, a busy timeout, and foreign-key
// enforcement so the single-writer SQLite store behaves safely.
const defaultSQLiteDSN = "file:ubag-gateway.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
const defaultFacadeMaxWait = 240 * time.Second

// Run starts the gateway and blocks until the context is cancelled or a fatal
// error occurs.
func Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Initialise contract-conformant JSON logging (§18.1). Must come before any
	// slog calls so all downstream logs use the redacting handler.
	logger := obs.InitLogger(ctx, os.Stderr)
	slog.SetDefault(logger)

	// Initialise OpenTelemetry tracing (§18 / Task 2.2). When UBAG_OTLP_ENDPOINT
	// is unset a no-op provider is installed — no external dependency required.
	tracerShutdown, err := obs.InitTracer(ctx)
	if err != nil {
		return fmt.Errorf("obs: init tracer: %w", err)
	}
	defer func() { _ = tracerShutdown(context.Background()) }()

	// Resolve the deployment profile (blueprint §4). The profile gates optional
	// surfaces and sets capacity ceilings via its §4.5 feature matrix.
	prof, err := profile.ParseOrDefault(os.Getenv("UBAG_PROFILE"))
	if err != nil {
		return fmt.Errorf("invalid profile configuration: %w", err)
	}
	feat := prof.Features()
	slog.Info("ubag gateway profile resolved",
		"profile", prof.String(),
		"job_backend", string(feat.JobBackend),
		"browser_session_pool_max", feat.BrowserSessionPoolMax,
		"semantic_cache", feat.SemanticCache.String(),
		"multi_tenant_rbac", feat.MultiTenantRBAC.String(),
		"sso", feat.SSO.String(),
		"scim", feat.SCIM.String(),
		"audit_delivery", string(feat.AuditDelivery),
		"tracing", string(feat.Tracing),
		"compliance_modes", feat.ComplianceModes.String(),
	)

	addr := getenv("UBAG_GATEWAY_ADDR", ":8080")
	rawDispatcher, err := newDispatcherFromEnv()
	if err != nil {
		return fmt.Errorf("invalid executor configuration: %w", err)
	}
	if closer, ok := rawDispatcher.(interface{ Close() }); ok {
		defer closer.Close()
	}
	breakerRegistry := resilience.NewRegistry(resilience.DefaultConfig())
	dispatcher := resilience.DispatcherMiddleware(rawDispatcher, breakerRegistry)
	jobs, idempotencyStore, db, storeKind, closeStores, err := newStoresFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("invalid store configuration: %w", err)
	}
	defer closeStores()

	// Advisory: the small+ profiles promise persistent jobs (§4.5). An ephemeral
	// in-memory store silently drops jobs on restart, so flag the mismatch.
	if prof.AtLeast(profile.Small) && storeKind == "memory" {
		slog.Warn("profile expects persistent jobs but UBAG_GATEWAY_STORE=memory; jobs will not survive restart",
			"profile", prof.String(), "expected_backend", string(feat.JobBackend))
	}

	artifactStore, err := newArtifactStoreFromEnv(storeKind, db)
	if err != nil {
		return fmt.Errorf("invalid artifact store configuration: %w", err)
	}
	webhookStore, err := newWebhookOutboxFromEnv(storeKind, db)
	if err != nil {
		return fmt.Errorf("invalid webhook outbox configuration: %w", err)
	}
	webhookPolicy := newWebhookURLPolicyFromEnv()
	// Set once the delivery worker exists; read by /v1/metrics through the
	// closure wired into httpapi.Config below.
	var webhookWorkerRunErrors func() uint64
	webhookMaxAttempts, err := intFromEnv("UBAG_WEBHOOK_MAX_ATTEMPTS", 8)
	if err != nil {
		return fmt.Errorf("invalid webhook retry configuration: %w", err)
	}
	webhookOutbox := &webhooks.JobOutbox{
		Store:       webhookStore,
		URLPolicy:   webhookPolicy,
		MaxAttempts: webhookMaxAttempts,
	}
	facadeMaxWait, err := durationFromMillisEnv("UBAG_FACADE_MAX_WAIT_MS", defaultFacadeMaxWait)
	if err != nil {
		return fmt.Errorf("invalid UBAG_FACADE_MAX_WAIT_MS: %w", err)
	}

	enterprise, err := newEnterpriseStoresFromEnv(ctx, storeKind, db)
	if err != nil {
		return fmt.Errorf("invalid enterprise store configuration: %w", err)
	}
	if enterprise.siemExporter != nil {
		enterprise.siemExporter.Start()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = enterprise.siemExporter.Close(shutdownCtx)
		}()
	}

	// Voice-session components: the shared store (memory by default keeps
	// the routes 501 only when UBAG_VOICE_STORE is explicitly disabled) and
	// the WebRTC media hub bridging client audio to the browser/audio
	// environment's relay process.
	voiceStore, voiceMedia, closeVoice, err := newVoiceComponentsFromEnv(ctx, storeKind, db, enterprise.topology)
	if err != nil {
		return fmt.Errorf("invalid voice configuration: %w", err)
	}
	defer closeVoice()
	voiceMetrics := voice.NewMediaCounters()
	if hub, ok := voiceMedia.(*voice.MediaHub); ok {
		hub.Metrics = voiceMetrics
		defer hub.Close()
		if voiceStore != nil {
			go reconcileVoiceMedia(ctx, hub, voiceStore)
		}
	}
	if voiceStore != nil {
		if err := voiceStore.Ready(ctx); err != nil {
			return fmt.Errorf("voice store not ready: %w", err)
		}
		// Lease sweeper: any replica may run it (CAS-guarded, idempotent);
		// it releases accounts pinned by crashed clients/replicas.
		go func() {
			ticker := time.NewTicker(voiceSweepInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if swept, err := voiceStore.SweepExpired(ctx, time.Now().UTC()); err != nil {
						slog.Error("voice lease sweep failed", "error", err)
					} else if len(swept) > 0 {
						slog.Warn("voice sessions terminated by lease expiry", "count", len(swept))
						if hub, ok := voiceMedia.(*voice.MediaHub); ok {
							for _, id := range swept {
								hub.Disconnect(id)
							}
						}
					}
				}
			}
		}()
		slog.Info("voice sessions enabled",
			"store", storeKind,
			"relay_map", os.Getenv("UBAG_VOICE_AUDIO_RELAY_MAP") != "",
			"relay_addr", os.Getenv("UBAG_VOICE_AUDIO_RELAY_ADDR"))
	}

	appJWTPublicKey, err := appJWTPublicKeyFromEnv()
	if err != nil {
		return fmt.Errorf("invalid app JWT configuration: %w", err)
	}
	if appJWTPublicKey != nil {
		slog.Info("app JWT authentication enabled: bearer RS256 tokens carry per-client (tenant_id, app_id) identity")
	}

	server := httpapi.NewServer(httpapi.Config{
		APIVersion:       getenv("UBAG_API_VERSION", httpapi.DefaultAPIVersion),
		Version:          getenv("UBAG_GATEWAY_VERSION", "0.0.0-dev"),
		BuildCommit:      getenv("UBAG_BUILD_COMMIT", "unknown"),
		AppSecret:        getenv("UBAG_APP_SECRET", ""),
		TenantID:         getenv("UBAG_TENANT_ID", ""),
		AppID:            getenv("UBAG_APP_ID", ""),
		ActorRole:        getenv("UBAG_ACTOR_ROLE", ""),
		AppJWTPublicKey:  appJWTPublicKey,
		DevCORSOrigin:    getenv("UBAG_DEV_CORS_ORIGIN", ""),
		Idempotency:      idempotencyStore,
		Jobs:             jobs,
		Executor:         dispatcher,
		Artifacts:        artifactStore,
		Webhooks:         webhookStore,
		WebhookURLPolicy: webhookPolicy,
		// Late-bound: the delivery worker is constructed further down, so the
		// metrics endpoint reads the counter through a closure rather than a
		// direct reference. Exposed as ubag_webhook_worker_run_errors_total so
		// "delivery has stopped retrying" is distinguishable from "no callbacks
		// are queued" - before this, Run's first error ended delivery silently.
		WebhookWorkerRunErrors: func() uint64 {
			if webhookWorkerRunErrors == nil {
				return 0
			}
			return webhookWorkerRunErrors()
		},
		FacadeMaxWait: facadeMaxWait,

		VoiceStore: voiceStore,
		VoiceMedia: voiceMedia,

		AdmissionKindCounts: func(ctx context.Context) (map[string]int, error) {
			if enterprise.admission == nil {
				return nil, nil
			}
			return enterprise.admission.LaneKindCounts(ctx, time.Now().UTC())
		},
		DBStats: func() sql.DBStats {
			if db == nil {
				return sql.DBStats{}
			}
			return db.Stats()
		},

		VoiceMetrics: voiceMetrics,
		// Provider voice is started by worker control jobs and a session is only
		// "connected" once the provider is verified ready. Disable only for
		// media-path development (UBAG_VOICE_PROVIDER_ACTIVATION=0).
		VoiceProviderActivation: voiceStore != nil && getenv("UBAG_VOICE_PROVIDER_ACTIVATION", "1") != "0",

		RateLimiter:       enterprise.rateLimiter,
		RateLimitResolver: enterprise.rateResolver,
		RateLimitEnabled:  enterprise.rateLimitEnabled,
		ResponseCache:     enterprise.responseCache,
		Workflows:         enterprise.workflows,
		SSO:               enterprise.sso,
		SCIM:              enterprise.scim,
		SIEMConfig:        enterprise.siemConfig,
		SIEMExporter:      enterprise.siemExporter,
		WebhookSecrets:    enterprise.webhookSecrets,
		Audit:             enterprise.audit,
		Sessions:          enterprise.sessions,
		SessionTTL:        enterprise.sessionTTL,
		PAT:               enterprise.pat,
		PATDefaultTTL:     enterprise.patDefaultTTL,
		Alerts:            enterprise.alerts,
		Topology:          enterprise.topology,
		Concurrency:       enterprise.concurrency,
		Conversations:     enterprise.conversations,
		RegionRouter:      enterprise.regionRouter,
		KillSwitch:        enterprise.killSwitch,
		MFA:               enterprise.mfaService,
		JITAdmin:          enterprise.jitAdmin,
		ABACEnforcer:      enterprise.abacEnforcer,
	})

	if workerConsumerEnabled() {
		consumer, err := newWorkerConsumerFromEnv(rawDispatcher, jobs, webhookOutbox, enterprise.alerts, enterprise.conversations, enterprise.concurrency, enterprise.topology, artifactStore, enterprise.admission)
		if err != nil {
			return fmt.Errorf("invalid worker consumer configuration: %w", err)
		}
		if err := consumer.Ready(ctx); err != nil {
			return fmt.Errorf("worker consumer is not ready: %w", err)
		}
		consumer.Metrics = server
		if closer, ok := consumer.Queue.(interface{ Close() }); ok {
			defer closer.Close()
		}
		// Terminate the warm-browser daemon (and any other closable runner) on
		// shutdown — previously the daemon process leaked a warm page per
		// SIGTERM because nothing ever closed the runner.
		if closer, ok := consumer.Runner.(interface{ Close() }); ok {
			defer closer.Close()
		}
		go func() {
			if err := consumer.Run(ctx); err != nil && err != context.Canceled {
				slog.Error("worker consumer stopped", "error", err)
			}
		}()
	}
	if staleJobReaperEnabled() {
		reaper := newStaleJobReaperFromEnv(jobs, enterprise.concurrency, webhookOutbox)
		go func() {
			if err := reaper.Run(ctx); err != nil && err != context.Canceled {
				slog.Error("stale-job reaper stopped", "error", err)
			}
		}()
	}

	// Spool retention sweeper: done/failed/cancelled file-spool envelopes are
	// never read again, so their growth is bounded here (env-configurable TTL
	// + max-count; both disabled turns the sweeper into a no-op).
	if spool, ok := rawDispatcher.(*executor.FileSpoolDispatcher); ok {
		retention, err := spoolRetentionFromEnv()
		if err != nil {
			return fmt.Errorf("invalid spool retention configuration: %w", err)
		}
		if retention.RetentionEnabled() {
			go func() {
				if err := spool.RunRetentionSweeper(ctx, retention); err != nil && err != context.Canceled {
					slog.Error("spool retention sweeper stopped", "error", err)
				}
			}()
			slog.Info("spool retention sweeper enabled",
				"ttl_seconds", int(retention.TTL.Seconds()),
				"max_entries", retention.MaxCount)
		}
	}
	if webhookWorkerEnabled() {
		worker, err := newWebhookWorkerFromEnv(webhookStore, webhookPolicy, breakerRegistry)
		if err != nil {
			return fmt.Errorf("invalid webhook worker configuration: %w", err)
		}
		if err := worker.Ready(ctx); err != nil {
			return fmt.Errorf("webhook worker is not ready: %w", err)
		}
		webhookWorkerRunErrors = worker.RunErrors
		go func() {
			if err := worker.Run(ctx); err != nil && err != context.Canceled {
				slog.Error("webhook worker stopped", "error", err)
			}
		}()
	}

	// Attachment dispatch-gate TTL sweeper: fails jobs stuck holding for their
	// attachment uploads past the TTL so their concurrency tokens are freed.
	go server.RunAttachmentSweeper(ctx)

	grpcServer := grpc.NewServer()
	ubagv1.RegisterJobServiceServer(grpcServer, grpcapi.NewServer(grpcapi.Config{
		APIVersion:  getenv("UBAG_API_VERSION", httpapi.DefaultAPIVersion),
		AppSecret:   getenv("UBAG_APP_SECRET", ""),
		TenantID:    getenv("UBAG_TENANT_ID", ""),
		AppID:       getenv("UBAG_APP_ID", ""),
		ActorRole:   getenv("UBAG_ACTOR_ROLE", ""),
		MFA:         enterprise.mfaService,
		ABAC:        enterprise.abacEnforcer,
		Jobs:        jobs,
		Idempotency: idempotencyStore,
		Executor:    dispatcher,
	}))
	reflection.Register(grpcServer)

	// Serve gRPC-Web on the existing HTTP mux so browser clients (e.g. the
	// SvelteKit console) can call the JobService. CORS is restricted to
	// loopback origins.
	wrappedGRPC := grpcweb.WrapServer(grpcServer,
		grpcweb.WithOriginFunc(loopbackOrigin),
		grpcweb.WithCorsForRegisteredEndpointsOnly(false),
	)
	if hub, ok := voiceMedia.(*voice.MediaHub); ok {
		hub.OnConnected = server.VoiceMediaConnected
		hub.OnEnded = server.VoiceMediaEnded
		hub.AuthorizeControl = server.VoiceControlAuthorizer()
	}
	baseHandler := server.Handler()
	gatewayHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wrappedGRPC.IsGrpcWebRequest(r) || wrappedGRPC.IsAcceptableGrpcCorsRequest(r) {
			wrappedGRPC.ServeHTTP(w, r)
			return
		}
		baseHandler.ServeHTTP(w, r)
	})

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           gatewayHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		// WriteTimeout is intentionally unset: the SSE stream
		// (/v1/sse/jobs/{id}) and the OpenAI facade long-poll
		// (UBAG_FACADE_MAX_WAIT_MS, default 240s) legitimately hold responses
		// open far beyond any sane WriteTimeout. A global WriteTimeout would
		// kill both mid-stream; splitting them onto their own listener is the
		// follow-up that unlocks a write deadline for everything else.
	}

	// Opt-in loopback-only pprof (UBAG_PPROF_ADDR). A bad value is logged, not
	// fatal: profiling is optional and must never take the gateway down.
	stopPprof, pprofErr := startPprof(os.Getenv("UBAG_PPROF_ADDR"))
	if pprofErr != nil {
		slog.Warn("pprof listener disabled", "error", pprofErr)
	}
	defer stopPprof()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpServer.ListenAndServe()
	}()

	grpcErr := make(chan error, 1)
	grpcAddr := strings.TrimSpace(os.Getenv("UBAG_GRPC_ADDR"))
	var grpcListener net.Listener
	if grpcAddr != "" {
		grpcListener, err = net.Listen("tcp", grpcAddr)
		if err != nil {
			return fmt.Errorf("failed to listen on gRPC address %q: %w", grpcAddr, err)
		}
		go func() {
			grpcErr <- grpcServer.Serve(grpcListener)
		}()
		slog.Info("starting ubag gateway grpc", "addr", grpcAddr)
	}

	slog.Info("starting ubag gateway", "addr", addr)
	select {
	case err := <-serverErr:
		if grpcListener != nil {
			grpcServer.Stop()
		}
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("gateway stopped: %w", err)
		}
		return nil
	case err := <-grpcErr:
		_ = httpServer.Close()
		if err != nil && err != grpc.ErrServerStopped {
			return fmt.Errorf("gateway grpc stopped: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	if grpcListener != nil {
		// GracefulStop waits for in-flight RPCs but has no built-in budget: a
		// stuck stream would hang shutdown forever. Force Stop() at half the
		// HTTP grace period.
		stopOnce := sync.Once{}
		timer := time.AfterFunc(shutdownGraceFromEnv()/2, func() {
			stopOnce.Do(func() { grpcServer.Stop() })
		})
		grpcServer.GracefulStop()
		timer.Stop()
	}
	// The grace period must comfortably exceed the facade long-poll budget
	// (UBAG_FACADE_MAX_WAIT_MS, default 240s) so an in-flight long-poll gets a
	// terminal answer instead of a connection reset on every deploy. It is
	// env-configurable so an operator can shorten it for fast restarts.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGraceFromEnv())
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("gateway shutdown failed: %w", err)
	}
	if err := <-serverErr; err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("gateway stopped: %w", err)
	}
	return nil
}

// loopbackOrigin reports whether a browser Origin header refers to a loopback
// host, restricting gRPC-Web CORS to local development consoles.
func loopbackOrigin(origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return false
	}
	host := origin
	if parsed, err := url.Parse(origin); err == nil && parsed.Host != "" {
		host = parsed.Hostname()
	}
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	default:
		return false
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}

// defaultShutdownGrace is the http.Server.Shutdown budget. The previous 10s
// value was shorter than the 240s facade long-poll, so every deploy reset
// in-flight long-polls; 25s still bounds restart time while letting the
// common (fast-terminal) long-polls drain.
const defaultShutdownGrace = 25 * time.Second

// shutdownGraceFromEnv reads UBAG_SHUTDOWN_GRACE_SECONDS. Invalid or
// non-positive values fall back to the default (an operator typo must not
// produce an unbounded or zero shutdown budget).
func shutdownGraceFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("UBAG_SHUTDOWN_GRACE_SECONDS"))
	if raw == "" {
		return defaultShutdownGrace
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultShutdownGrace
	}
	return time.Duration(value) * time.Second
}

// spoolRetentionFromEnv reads the terminal-state spool retention bounds.
// UBAG_SPOOL_RETENTION_TTL_SECONDS (default 7 days) deletes terminal spool
// envelopes older than the TTL; UBAG_SPOOL_RETENTION_MAX (default 10000)
// caps how many terminal envelopes are kept, oldest evicted first. 0 or a
// negative value disables that individual bound; disabling both disables the
// sweeper entirely.
func spoolRetentionFromEnv() (executor.SpoolRetentionConfig, error) {
	retention := executor.SpoolRetentionConfig{
		TTL:      executor.DefaultSpoolRetentionTTL,
		MaxCount: executor.DefaultSpoolRetentionMax,
	}
	if raw := strings.TrimSpace(os.Getenv("UBAG_SPOOL_RETENTION_TTL_SECONDS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return retention, fmt.Errorf("UBAG_SPOOL_RETENTION_TTL_SECONDS must be an integer number of seconds")
		}
		if value <= 0 {
			retention.TTL = 0
		} else {
			retention.TTL = time.Duration(value) * time.Second
		}
	}
	if raw := strings.TrimSpace(os.Getenv("UBAG_SPOOL_RETENTION_MAX")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return retention, fmt.Errorf("UBAG_SPOOL_RETENTION_MAX must be an integer")
		}
		if value <= 0 {
			retention.MaxCount = 0
		} else {
			retention.MaxCount = value
		}
	}
	return retention, nil
}

func newDispatcherFromEnv() (executor.Dispatcher, error) {
	mode := strings.ToLower(strings.TrimSpace(getenv("UBAG_EXECUTOR_MODE", "noop")))
	switch mode {
	case "", "noop", "disabled":
		return executor.NewNoopDispatcher(), nil
	case "file":
		spoolDir := strings.TrimSpace(os.Getenv("UBAG_EXECUTOR_SPOOL_DIR"))
		if spoolDir == "" {
			return nil, fmt.Errorf("UBAG_EXECUTOR_SPOOL_DIR is required when UBAG_EXECUTOR_MODE=file")
		}
		dispatcher := executor.NewFileSpoolDispatcher(spoolDir)
		// A previous process may have died mid-RunOnce (upgrade, crash, killed
		// window): its leases are stranded in spool/leased forever unless they
		// are returned to pending at startup.
		if recovered, err := dispatcher.RecoverOrphanLeases(); err != nil {
			slog.Warn("file spool lease recovery failed", "error", err)
		} else if recovered > 0 {
			slog.Info("recovered orphaned spool leases", "count", recovered)
		}
		return dispatcher, nil
	case "nats":
		url, streamName, subject := natsDispatcherConfigFromEnv()
		return executor.NewNATSDispatcher(url, streamName, subject), nil
	default:
		return nil, fmt.Errorf("unsupported UBAG_EXECUTOR_MODE %q", mode)
	}
}

func natsDispatcherConfigFromEnv() (string, string, string) {
	url := firstEnv("UBAG_NATS_URL", "NATS_URL")
	if url == "" {
		url = "nats://127.0.0.1:4222"
	}
	streamName := firstEnv("UBAG_NATS_STREAM")
	if streamName == "" {
		streamName = "UBAG_JOBS"
	}
	subject := firstEnv("UBAG_NATS_SUBJECT")
	if subject == "" {
		subject = "ubag.jobs"
	}
	return url, streamName, subject
}

func newStoresFromEnv(ctx context.Context) (jobstore.Store, idempotency.Service, *sql.DB, string, func(), error) {
	mode := strings.ToLower(strings.TrimSpace(getenv("UBAG_GATEWAY_STORE", "memory")))
	switch mode {
	case "", "memory", "in_memory":
		return jobstore.NewMemoryStore(), idempotency.NewMemoryStore(idempotencyTTLFromEnv()), nil, "memory", func() {}, nil
	case "postgres", "postgresql":
		dsn := firstEnv("UBAG_POSTGRES_DSN", "UBAG_DATABASE_URL")
		if dsn == "" {
			return nil, nil, nil, "", nil, fmt.Errorf("UBAG_POSTGRES_DSN or UBAG_DATABASE_URL is required when UBAG_GATEWAY_STORE=postgres")
		}
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, nil, nil, "", nil, err
		}
		configureDBPoolFromEnv(db)
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, nil, nil, "", nil, err
		}
		return jobstore.NewPostgresStore(db), idempotency.NewPostgresStore(db, idempotencyTTLFromEnv()), db, "postgres", func() { _ = db.Close() }, nil
	case "sqlite", "sqlite3":
		dsn := strings.TrimSpace(getenv("UBAG_SQLITE_DSN", defaultSQLiteDSN))
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, nil, nil, "", nil, err
		}
		// SQLite is a single-writer database; serialize access through one
		// connection so concurrent writes never trip SQLITE_BUSY.
		db.SetMaxOpenConns(1)
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, nil, nil, "", nil, err
		}
		if err := sqlitestore.Apply(ctx, db); err != nil {
			_ = db.Close()
			return nil, nil, nil, "", nil, err
		}
		return jobstore.NewSQLiteStore(db), idempotency.NewSQLiteStore(db, idempotencyTTLFromEnv()), db, "sqlite", func() { _ = db.Close() }, nil
	default:
		return nil, nil, nil, "", nil, fmt.Errorf("unsupported UBAG_GATEWAY_STORE %q", mode)
	}
}

// newArtifactStoreFromEnv creates the artifact store based on UBAG_ARTIFACT_STORE.
// storeKind identifies the runtime store (memory/postgres/sqlite) so artifact
// metadata can be persisted in the matching database when available.
func newArtifactStoreFromEnv(storeKind string, db *sql.DB) (artifacts.ArtifactStore, error) {
	mode := strings.ToLower(strings.TrimSpace(getenv("UBAG_ARTIFACT_STORE", "memory")))
	switch mode {
	case "", "memory", "in_memory":
		return artifacts.NewMemoryArtifactStore(), nil
	case "localfs", "local", "filesystem":
		rootDir := firstEnv("UBAG_ARTIFACT_DIR", "UBAG_ARTIFACT_LOCALFS_DIR")
		if rootDir == "" {
			return nil, fmt.Errorf("UBAG_ARTIFACT_DIR is required when UBAG_ARTIFACT_STORE=localfs")
		}
		meta := artifactMetaForStore(storeKind, db)
		return artifacts.NewLocalFSArtifactStore(rootDir, meta)
	case "minio", "s3":
		endpoint := firstEnv("UBAG_MINIO_ENDPOINT", "MINIO_ENDPOINT")
		if endpoint == "" {
			return nil, fmt.Errorf("UBAG_MINIO_ENDPOINT is required when UBAG_ARTIFACT_STORE=minio")
		}
		accessKey := firstEnv("UBAG_MINIO_ACCESS_KEY", "MINIO_ROOT_USER")
		secretKey := firstEnv("UBAG_MINIO_SECRET_KEY", "MINIO_ROOT_PASSWORD")
		bucket := firstEnv("UBAG_MINIO_BUCKET")
		if bucket == "" {
			bucket = "ubag-artifacts"
		}
		useSSL := strings.EqualFold(strings.TrimSpace(os.Getenv("UBAG_MINIO_USE_SSL")), "true")

		meta := artifactMetaForStore(storeKind, db)
		return artifacts.NewMinIOArtifactStore(endpoint, accessKey, secretKey, bucket, useSSL, meta)
	default:
		return nil, fmt.Errorf("unsupported UBAG_ARTIFACT_STORE %q", mode)
	}
}

// artifactMetaForStore returns the artifact metadata backend matching the
// active runtime store, or nil to fall back to in-memory metadata.
func artifactMetaForStore(storeKind string, db *sql.DB) artifacts.ArtifactMeta {
	if db == nil {
		return nil
	}
	switch storeKind {
	case "sqlite":
		return artifacts.NewSQLiteArtifactMeta(db)
	case "postgres":
		return artifacts.NewPostgresArtifactMeta(db)
	default:
		return nil
	}
}

func newWebhookOutboxFromEnv(storeKind string, db *sql.DB) (webhooks.OutboxStore, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_WEBHOOK_OUTBOX")))
	if mode == "" {
		switch storeKind {
		case "postgres":
			mode = "postgres"
		case "sqlite":
			mode = "sqlite"
		default:
			mode = "memory"
		}
	}
	switch mode {
	case "memory", "in_memory":
		return webhooks.NewMemoryStore(), nil
	case "postgres", "postgresql":
		if db == nil || storeKind != "postgres" {
			return nil, fmt.Errorf("UBAG_GATEWAY_STORE=postgres is required when UBAG_WEBHOOK_OUTBOX=postgres")
		}
		return webhooks.NewPostgresStore(db), nil
	case "sqlite", "sqlite3":
		if db == nil || storeKind != "sqlite" {
			return nil, fmt.Errorf("UBAG_GATEWAY_STORE=sqlite is required when UBAG_WEBHOOK_OUTBOX=sqlite")
		}
		return webhooks.NewSQLiteStore(db), nil
	default:
		return nil, fmt.Errorf("unsupported UBAG_WEBHOOK_OUTBOX %q", mode)
	}
}

// enterpriseStores bundles the optional gateway enterprise components. Every
// field is nil-safe: the HTTP server returns clean 501/empty results when a
// component is nil and the rate-limit middleware is a pass-through.
type enterpriseStores struct {
	rateLimiter      ratelimit.Limiter
	rateResolver     *ratelimit.PolicyResolver
	rateLimitEnabled bool
	responseCache    *responsecache.Cache
	workflows        workflow.Store
	sso              sso.ConfigStore
	scim             scim.Store
	siemConfig       siem.ConfigStore
	siemExporter     *siem.Exporter
	webhookSecrets   httpapi.WebhookSecretStore
	audit            audit.Store
	sessions         session.Store
	sessionTTL       time.Duration
	pat              pat.Store
	patDefaultTTL    time.Duration
	alerts           *alerts.Manager
	topology         topology.Store
	concurrency      *topology.ConcurrencyRegistry
	admission        *topology.SQLTokenBackend
	conversations    *conversations.Manager

	// abacEnforcer is nil unless an operator supplies UBAG_ABAC_BUNDLE. Nil means
	// the HTTP server skips ABAC entirely, so wiring it in is an exact no-op for
	// existing callers (RBAC remains the only gate).
	abacEnforcer *abac.Enforcer

	// Phase 9 — multi-region and enterprise auth (gated on GeoReplication=On or env override)
	regionRegistry *region.Registry
	regionRouter   *region.Router
	killSwitch     *region.KillSwitch
	mfaService     *mfa.Service
	jitAdmin       jitadmin.Store
}

// newEnterpriseStoresFromEnv constructs the optional enterprise components.
// SQL-backed stores are used only for the sqlite store kind (and postgres for
// rate limiting, which has a native backend); all other kinds fall back to
// in-memory implementations so the gateway always boots.
func newEnterpriseStoresFromEnv(ctx context.Context, storeKind string, db *sql.DB) (enterpriseStores, error) {
	var out enterpriseStores

	// Rate limiting (gated by UBAG_RATE_LIMIT_ENABLED, default off).
	out.rateResolver = ratelimit.DefaultPolicyResolver()
	out.rateLimitEnabled = envBool("UBAG_RATE_LIMIT_ENABLED")
	rlStore, err := storekit.Pick(
		storekit.Kind(storeKind), db, "rate limit",
		func(db *sql.DB) (ratelimit.Store, error) { return ratelimit.NewSQLiteStore(ctx, db) },
		func(db *sql.DB) (ratelimit.Store, error) { return ratelimit.NewPostgresStore(ctx, db) },
		func() ratelimit.Store { return ratelimit.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, fmt.Errorf("rate limit store: %w", err)
	}
	out.rateLimiter = ratelimit.New(rlStore, out.rateResolver.Default())

	// Response cache (gated by UBAG_CACHE_ENABLED, default off).
	cacheEnabled := envBool("UBAG_CACHE_ENABLED")
	cacheTTL, err := durationFromMillisEnv("UBAG_CACHE_TTL_MS", 5*time.Minute)
	if err != nil {
		return enterpriseStores{}, fmt.Errorf("invalid UBAG_CACHE_TTL_MS: %w", err)
	}
	cacheStore, err := storekit.Pick(
		storekit.Kind(storeKind), db, "response cache",
		func(db *sql.DB) (responsecache.Store, error) {
			sqliteCache := responsecache.NewSQLiteStore(db)
			if err := sqliteCache.EnsureSchema(ctx); err != nil {
				return nil, fmt.Errorf("response cache sqlite schema: %w", err)
			}
			return sqliteCache, nil
		},
		func(db *sql.DB) (responsecache.Store, error) {
			pgCache := responsecache.NewPostgresStore(db)
			if err := pgCache.Ready(ctx); err != nil {
				return nil, fmt.Errorf("response cache postgres store: %w", err)
			}
			return pgCache, nil
		},
		func() responsecache.Store { return responsecache.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}
	out.responseCache = responsecache.New(cacheStore, responsecache.Options{TTL: cacheTTL, Enabled: cacheEnabled})

	// Workflow orchestration store.
	out.workflows, err = storekit.Pick(
		storekit.Kind(storeKind), db, "workflow",
		func(db *sql.DB) (workflow.Store, error) {
			wfStore := workflow.NewSQLiteStore(db)
			if err := wfStore.Migrate(ctx); err != nil {
				return nil, fmt.Errorf("workflow sqlite migrate: %w", err)
			}
			return wfStore, nil
		},
		func(db *sql.DB) (workflow.Store, error) {
			wfStore := workflow.NewPostgresStore(db)
			if err := wfStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("workflow postgres store: %w", err)
			}
			return wfStore, nil
		},
		func() workflow.Store { return workflow.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}

	// SSO configuration store.
	out.sso, err = storekit.Pick(
		storekit.Kind(storeKind), db, "sso",
		func(db *sql.DB) (sso.ConfigStore, error) {
			ssoStore := sso.NewSQLiteStore(db)
			if err := ssoStore.Migrate(ctx); err != nil {
				return nil, fmt.Errorf("sso sqlite migrate: %w", err)
			}
			return ssoStore, nil
		},
		func(db *sql.DB) (sso.ConfigStore, error) {
			ssoStore := sso.NewPostgresStore(db)
			if err := ssoStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("sso postgres store: %w", err)
			}
			return ssoStore, nil
		},
		func() sso.ConfigStore { return sso.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}

	// SCIM provisioning store.
	out.scim, err = storekit.Pick(
		storekit.Kind(storeKind), db, "scim",
		func(db *sql.DB) (scim.Store, error) {
			scimStore, err := scim.NewSQLiteStore(db)
			if err != nil {
				return nil, fmt.Errorf("scim sqlite store: %w", err)
			}
			return scimStore, nil
		},
		func(db *sql.DB) (scim.Store, error) {
			scimStore, err := scim.NewPostgresStore(db)
			if err != nil {
				return nil, fmt.Errorf("scim postgres store: %w", err)
			}
			if err := scimStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("scim postgres store: %w", err)
			}
			return scimStore, nil
		},
		func() scim.Store { return scim.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}

	// SIEM sink configuration store.
	out.siemConfig, err = storekit.Pick(
		storekit.Kind(storeKind), db, "siem",
		func(db *sql.DB) (siem.ConfigStore, error) {
			siemStore := siem.NewSQLiteStore(db)
			if err := siemStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("siem sqlite schema: %w", err)
			}
			return siemStore, nil
		},
		func(db *sql.DB) (siem.ConfigStore, error) {
			siemStore := siem.NewPostgresStore(db)
			if err := siemStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("siem postgres schema: %w", err)
			}
			return siemStore, nil
		},
		func() siem.ConfigStore { return siem.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}

	// SIEM exporter: only built when a file sink path is configured.
	if path := strings.TrimSpace(os.Getenv("UBAG_SIEM_FILE_PATH")); path != "" {
		exporter, err := siem.NewExporter(siem.ExporterConfig{
			Sinks: []siem.Sink{siem.NewFileSink(path)},
		})
		if err != nil {
			return enterpriseStores{}, fmt.Errorf("siem exporter: %w", err)
		}
		out.siemExporter = exporter
	}

	// Webhook secret rotation store.
	out.webhookSecrets, err = storekit.Pick(
		storekit.Kind(storeKind), db, "webhook secrets",
		func(db *sql.DB) (httpapi.WebhookSecretStore, error) {
			secretStore := httpapi.NewSQLiteWebhookSecretStore(db)
			if err := secretStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("webhook secret sqlite schema: %w", err)
			}
			return secretStore, nil
		},
		func(db *sql.DB) (httpapi.WebhookSecretStore, error) {
			secretStore := httpapi.NewPostgresWebhookSecretStore(db)
			if err := secretStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("webhook secret postgres schema: %w", err)
			}
			return secretStore, nil
		},
		func() httpapi.WebhookSecretStore { return httpapi.NewMemoryWebhookSecretStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}

	// Audit log store (Merkle-chained, per-tenant).
	out.audit, err = storekit.Pick(
		storekit.Kind(storeKind), db, "audit",
		func(db *sql.DB) (audit.Store, error) {
			auditStore := audit.NewSQLiteStore(db)
			if err := auditStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("audit sqlite schema: %w", err)
			}
			return auditStore, nil
		},
		func(db *sql.DB) (audit.Store, error) {
			auditStore := audit.NewPostgresStore(db)
			if err := auditStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("audit postgres schema: %w", err)
			}
			return auditStore, nil
		},
		func() audit.Store { return audit.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}

	// Server-side SSO session store.
	sessionTTL, err := durationFromMillisEnv("UBAG_SESSION_TTL_MS", time.Hour)
	if err != nil {
		return enterpriseStores{}, err
	}
	out.sessionTTL = sessionTTL
	out.sessions, err = storekit.Pick(
		storekit.Kind(storeKind), db, "session",
		func(db *sql.DB) (session.Store, error) {
			sessionStore := session.NewSQLiteStore(db)
			if err := sessionStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("session sqlite schema: %w", err)
			}
			return sessionStore, nil
		},
		func(db *sql.DB) (session.Store, error) {
			sessionStore := session.NewPostgresStore(db)
			if err := sessionStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("session postgres schema: %w", err)
			}
			return sessionStore, nil
		},
		func() session.Store { return session.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}

	// Personal Access Token store (§11): opaque ubag_pat_ bearer tokens issued
	// via POST /v1/auth/pat (superadmin only). Opt-in — UBAG_PAT_ENABLED gates
	// the whole feature so a credential-issuance endpoint is never live by
	// accident; when disabled the store stays nil and the route returns 501. The
	// store follows the gateway store kind so issued tokens survive restarts on
	// sqlite/postgres (the in-memory store loses them on restart).
	if envBool("UBAG_PAT_ENABLED") {
		patTTL, err := durationFromMillisEnv("UBAG_PAT_DEFAULT_TTL_MS", 0)
		if err != nil {
			return enterpriseStores{}, fmt.Errorf("invalid UBAG_PAT_DEFAULT_TTL_MS: %w", err)
		}
		out.patDefaultTTL = patTTL
		out.pat, err = storekit.Pick(
			storekit.Kind(storeKind), db, "pat",
			func(db *sql.DB) (pat.Store, error) {
				patStore := pat.NewSQLiteStore(db)
				if err := patStore.Ready(ctx); err != nil {
					return nil, fmt.Errorf("pat sqlite schema: %w", err)
				}
				return patStore, nil
			},
			func(db *sql.DB) (pat.Store, error) {
				patStore := pat.NewPostgresStore(db)
				if err := patStore.Ready(ctx); err != nil {
					return nil, fmt.Errorf("pat postgres schema: %w", err)
				}
				return patStore, nil
			},
			func() pat.Store { return pat.NewMemoryStore() },
		)
		if err != nil {
			return enterpriseStores{}, err
		}
	}

	// Human-in-the-loop manual-action alert store + notification sink.
	sink, summary := alerts.SinkFromEnv(slog.Default(), storeKind)
	alertStore, err := storekit.Pick(
		storekit.Kind(storeKind), db, "alerts",
		func(db *sql.DB) (alerts.Store, error) {
			sqliteAlerts := alerts.NewSQLiteStore(db)
			if err := sqliteAlerts.Ready(ctx); err != nil {
				return nil, fmt.Errorf("alerts sqlite schema: %w", err)
			}
			return sqliteAlerts, nil
		},
		func(db *sql.DB) (alerts.Store, error) {
			postgresAlerts := alerts.NewPostgresStore(db)
			if err := postgresAlerts.Ready(ctx); err != nil {
				return nil, fmt.Errorf("alerts postgres schema: %w", err)
			}
			return postgresAlerts, nil
		},
		func() alerts.Store { return alerts.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}
	out.alerts = alerts.NewManager(alertStore, sink, slog.Default(), summary)

	if envBool("UBAG_CONVERSATIONS_ENABLED") {
		conversationStore, err := storekit.Pick(
			storekit.Kind(storeKind), db, "conversations",
			func(db *sql.DB) (conversations.Store, error) {
				sqliteConversations := conversations.NewSQLiteStore(db)
				if err := sqliteConversations.Ready(ctx); err != nil {
					return nil, fmt.Errorf("conversations sqlite schema: %w", err)
				}
				return sqliteConversations, nil
			},
			func(db *sql.DB) (conversations.Store, error) {
				postgresConversations := conversations.NewPostgresStore(db)
				if err := postgresConversations.Ready(ctx); err != nil {
					return nil, fmt.Errorf("conversations postgres schema: %w", err)
				}
				return postgresConversations, nil
			},
			func() conversations.Store { return conversations.NewMemoryStore() },
		)
		if err != nil {
			return enterpriseStores{}, err
		}
		out.conversations = conversations.NewManager(conversationStore, slog.Default(), storeKind)
	}

	// Read-only v2.1 browser topology store + adaptive-concurrency view.
	out.topology, err = storekit.Pick(
		storekit.Kind(storeKind), db, "browser topology",
		func(db *sql.DB) (topology.Store, error) {
			topologyStore := topology.NewSQLiteStore(db)
			if err := topologyStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("browser topology sqlite schema: %w", err)
			}
			return topologyStore, nil
		},
		func(db *sql.DB) (topology.Store, error) {
			topologyStore := topology.NewPostgresStore(db)
			if err := topologyStore.Ready(ctx); err != nil {
				return nil, fmt.Errorf("browser topology postgres schema: %w", err)
			}
			return topologyStore, nil
		},
		func() topology.Store { return topology.NewMemoryStore() },
	)
	if err != nil {
		return enterpriseStores{}, err
	}
	// The concurrency registry is always available; it is populated by the
	// worker-event ingestion path and never mutated via HTTP.
	out.concurrency = topology.NewConcurrencyRegistry()
	out.concurrency.SetDefaultLaneCap(envPositiveInt("UBAG_ADMISSION_DEFAULT_LANE_CAP"))
	// Shared admission: with a SQL store, in-flight tokens live in the
	// database so every replica admits against one authority (memory mode
	// keeps the process-local counters, which is correct for one process).
	// UBAG_ADMISSION_SHARED is a kill-switch: unset/true keeps the live
	// behaviour; false falls back to process-local counters and disables
	// per-job execution leases (they share this backend).
	if db != nil && (storeKind == "sqlite" || storeKind == "postgres") && envBoolDefaultTrue("UBAG_ADMISSION_SHARED") {
		backend := topology.NewSQLiteTokenBackend(db)
		if storeKind == "postgres" {
			backend = topology.NewPostgresTokenBackend(db)
		}
		if err := backend.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("admission token store: %w", err)
		}
		out.concurrency.UseBackend(backend, topology.LaneLimits{
			App:    envPositiveInt("UBAG_ADMISSION_MAX_INFLIGHT_PER_APP"),
			Tenant: envPositiveInt("UBAG_ADMISSION_MAX_INFLIGHT_PER_TENANT"),
			Global: envPositiveInt("UBAG_ADMISSION_MAX_INFLIGHT_GLOBAL"),
		})
		out.admission = backend
		go func() {
			ticker := time.NewTicker(admissionSweepInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if n, err := backend.SweepExpired(ctx, time.Now().UTC()); err != nil {
						slog.Error("admission token sweep failed", "error", err)
					} else if n > 0 {
						slog.Warn("expired unassociated admission tokens released", "count", n)
					}
				}
			}
		}()
	}

	// ABAC policy bundle (opt-in). The enforcer stays nil unless an operator
	// supplies a bundle, so activating this machinery cannot deny any request
	// that RBAC already allows — critical because a mis-scoped rule would 403
	// job:create and silently kill BOTH failover providers. A malformed bundle
	// fails startup loudly rather than booting silently unenforced.
	if path := strings.TrimSpace(os.Getenv("UBAG_ABAC_BUNDLE")); path != "" {
		bundle, err := abac.LoadBundleFromFile(path)
		if err != nil {
			return enterpriseStores{}, fmt.Errorf("abac bundle: %w", err)
		}
		enforcer, err := abac.NewEnforcer(bundle)
		if err != nil {
			return enterpriseStores{}, fmt.Errorf("abac enforcer: %w", err)
		}
		out.abacEnforcer = enforcer
		slog.Info("abac policy bundle loaded", "path", path, "rules", len(bundle.Rules))
	}

	// Phase 9 — resolve the deployment profile to gate region and auth components.
	prof, _ := profile.ParseOrDefault(os.Getenv("UBAG_PROFILE"))
	feat := prof.Features()

	// Region routing + kill switch (gated on GeoReplication=On or env override).
	geoEnabled := feat.GeoReplication == profile.On ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("UBAG_ENABLE_GEO_REPLICATION")), "1")
	if geoEnabled {
		memStateStore := region.NewMemoryStateStore()
		registry := region.NewRegistry(memStateStore)
		pinResolver := region.NewMemoryResolver(nil)
		router := region.NewRouter(pinResolver, registry, region.CurrentRegion)
		ks := region.NewKillSwitch(registry, region.CurrentRegion, out.audit)
		out.regionRegistry = registry
		out.regionRouter = router
		out.killSwitch = ks
		slog.Info("phase9: region routing enabled", "region", string(region.CurrentRegion()))
	}

	// MFA + JIT admin elevation (gated on SSO.Enabled() or env override).
	mfaEnabled := feat.SSO.Enabled() ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("UBAG_ENABLE_MFA")), "1")
	if mfaEnabled {
		mfaStore := mfa.NewMemoryStore()
		out.mfaService = &mfa.Service{
			Store: mfaStore,
			Clock: time.Now,
		}
		out.jitAdmin = jitadmin.NewMemoryStore()
		slog.Info("phase9: MFA and JIT admin elevation enabled")
	}

	// Wrap audit store with SIEM bridge when a SIEM exporter is configured so
	// every appended audit record is also forwarded to the exporter in real-time.
	if out.audit != nil && out.siemExporter != nil {
		out.audit = audit.NewBridgeStore(out.audit, out.siemExporter.Enqueue)
	}

	return out, nil
}

func idempotencyTTLFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("UBAG_IDEMPOTENCY_TTL_HOURS"))
	if raw == "" {
		return 24 * time.Hour
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(value) * time.Hour
}

// Default Postgres pool caps applied when the UBAG_DATABASE_* env vars are
// unset. MaxOpenConns=0 means unlimited, so without these a burst of
// concurrent requests can open an unbounded number of Postgres connections and
// exhaust the server's max_connections. The env vars remain the override.
const (
	defaultPostgresMaxOpenConns = 20
	defaultPostgresMaxIdleConns = 5
)

func configureDBPoolFromEnv(db *sql.DB) {
	if value := positiveIntEnv("UBAG_DATABASE_MAX_OPEN_CONNS"); value > 0 {
		db.SetMaxOpenConns(value)
	} else {
		db.SetMaxOpenConns(defaultPostgresMaxOpenConns)
	}
	if value := positiveIntEnv("UBAG_DATABASE_MAX_IDLE_CONNS"); value > 0 {
		db.SetMaxIdleConns(value)
	} else {
		db.SetMaxIdleConns(defaultPostgresMaxIdleConns)
	}
	if value := positiveIntEnv("UBAG_DATABASE_CONN_MAX_LIFETIME_SECONDS"); value > 0 {
		db.SetConnMaxLifetime(time.Duration(value) * time.Second)
	}
}

func positiveIntEnv(key string) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func workerConsumerEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_WORKER_CONSUMER_ENABLED")))
	return value == "1" || value == "true" || value == "yes"
}

// appJWTPublicKeyFromEnv loads the RS256 public key that enables App JWT
// authentication (§11). UBAG_APP_JWT_PUBLIC_KEY carries the PEM inline
// (literal \n sequences are accepted so single-line .env values work); when it
// is empty, UBAG_APP_JWT_PUBLIC_KEY_FILE names a PEM file (e.g. a mounted
// secret). Both unset disables the feature (nil key, app-secret auth only). A
// configured-but-unusable key fails startup instead of silently running
// without JWT auth.
func appJWTPublicKeyFromEnv() (*rsa.PublicKey, error) {
	inline := strings.TrimSpace(os.Getenv("UBAG_APP_JWT_PUBLIC_KEY"))
	file := strings.TrimSpace(os.Getenv("UBAG_APP_JWT_PUBLIC_KEY_FILE"))

	var pemText string
	switch {
	case inline != "":
		pemText = strings.ReplaceAll(inline, `\n`, "\n")
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("app JWT public key file %q: %w", file, err)
		}
		pemText = string(raw)
	default:
		return nil, nil
	}

	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("app JWT public key is not valid PEM")
	}
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		rsaKey, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("app JWT public key must be an RSA key, got %T", parsed)
		}
		return rsaKey, nil
	}
	rsaKey, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("app JWT public key is not a PKIX or PKCS#1 RSA public key: %w", err)
	}
	return rsaKey, nil
}

// workerDaemonEnabled reports whether jobs run through ONE long-lived worker
// that keeps browser pages warm, instead of a worker spawned per job.
//
// Opt-in, and it stays that way: warm reuse changes how a live browser session
// is driven for a radiology product, so being deployed must never be enough to
// turn it on.
func workerDaemonEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_WORKER_DAEMON")))
	return value == "1" || value == "true" || value == "yes"
}

var warmDaemonTargets = map[string]struct{}{
	"chatgpt_web":    {},
	"deepseek_web":   {},
	"gemini_web":     {},
	"mistral_lechat": {},
	"duckai_web":     {},
}

// targetWorkerRunner keeps non-live adapters on the normal per-job worker even
// when warm browser reuse is enabled. The Python daemon intentionally supports
// only targets in PROVIDER_SELECTORS; routing mock/generic jobs into it would
// break the text-job compatibility path.
type targetWorkerRunner struct {
	daemon   executor.WorkerRunner
	fallback executor.WorkerRunner
}

func (r *targetWorkerRunner) RunWorker(
	ctx context.Context,
	envelope executor.DispatchEnvelope,
) ([]jobstore.WorkerEvent, error) {
	// Voice control jobs drive the browser through their own CDP client and
	// must never run on the warm daemon's shared Playwright thread.
	if _, ok := warmDaemonTargets[strings.TrimSpace(envelope.Job.Target)]; ok && !jobcore.IsReservedCommandType(envelope.Job.CommandType) {
		return r.daemon.RunWorker(ctx, envelope)
	}
	return r.fallback.RunWorker(ctx, envelope)
}

// Close terminates the wrapped runners so gateway shutdown does not leak the
// warm-browser daemon process.
func (r *targetWorkerRunner) Close() {
	for _, runner := range []executor.WorkerRunner{r.daemon, r.fallback} {
		if closer, ok := runner.(interface{ Close() }); ok {
			closer.Close()
		}
	}
}

// buildWorkerRunner picks the per-job runner (default) or the warm-browser
// daemon (UBAG_WORKER_DAEMON).
func buildWorkerRunner(
	python string,
	script string,
	maxRuntime time.Duration,
	artifactStore artifacts.ArtifactStore,
	jobs jobstore.Store,
) (executor.WorkerRunner, error) {
	if !workerDaemonEnabled() {
		return executor.ProcessWorkerRunner{
			Python:     python,
			Script:     script,
			MaxRuntime: maxRuntime,
			Artifacts:  artifactStore,
			Jobs:       jobs,
		}, nil
	}

	// The daemon has its own entrypoint. Refuse rather than fall back to the
	// per-job script: that process exits after one job, so every job would
	// restart it and warm reuse would look enabled while doing nothing.
	daemonScript, err := resolveWorkerScriptPath(strings.TrimSpace(os.Getenv("UBAG_WORKER_DAEMON_SCRIPT")))
	if err != nil {
		return nil, fmt.Errorf("worker daemon script: %w", err)
	}
	if strings.TrimSpace(daemonScript) == "" {
		return nil, fmt.Errorf(
			"UBAG_WORKER_DAEMON is enabled but UBAG_WORKER_DAEMON_SCRIPT is not set " +
				"(expected apps/worker/run_worker_daemon.py)")
	}
	slog.Warn("worker daemon enabled: browser pages are reused between jobs",
		"script", daemonScript)
	daemon := &executor.DaemonWorkerRunner{
		Python:     python,
		Script:     daemonScript,
		MaxRuntime: maxRuntime,
		Artifacts:  artifactStore,
	}
	return &targetWorkerRunner{
		daemon: daemon,
		fallback: executor.ProcessWorkerRunner{
			Python:     python,
			Script:     script,
			MaxRuntime: maxRuntime,
			Artifacts:  artifactStore,
			Jobs:       jobs,
		},
	}, nil
}

// guardLiveWorkerConcurrency warns when several per-job live workers would run
// at once: they share one per-role tab registry and each worker's stale-page
// cleanup closes its siblings' live tabs. Clamping to 1 is opt-in via
// UBAG_WORKER_LIVE_CONCURRENCY_GUARD so existing deployments are unchanged.
func guardLiveWorkerConcurrency(concurrency int, script string) int {
	if concurrency <= 1 || filepath.Base(script) != "run_live_worker.py" {
		return concurrency
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_WORKER_LIVE_CONCURRENCY_GUARD")))
	clamp := v == "1" || v == "true" || v == "yes"
	slog.Warn("UBAG_WORKER_CONCURRENCY>1 with per-job live workers: siblings close each other's live tabs",
		"requested", concurrency, "script", filepath.Base(script), "clamped", clamp)
	if clamp {
		return 1
	}
	return concurrency
}

func staleJobReaperEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_JOB_REAPER_ENABLED")))
	return value == "1" || value == "true" || value == "yes"
}

// newStaleJobReaperFromEnv builds the reaper. It is opt-in (disabled unless
// UBAG_JOB_REAPER_ENABLED is set). UBAG_JOB_MAX_LIFETIME_SECONDS is the fallback
// idle deadline for jobs without an explicit options.timeout_seconds (default
// 3600s); UBAG_JOB_REAPER_INTERVAL_SECONDS is the sweep cadence (default 60s).
func newStaleJobReaperFromEnv(jobs jobstore.Store, concurrency *topology.ConcurrencyRegistry, notifier executor.TerminalJobNotifier) *executor.StaleJobReaper {
	maxLifetimeSecs := positiveIntEnv("UBAG_JOB_MAX_LIFETIME_SECONDS")
	if maxLifetimeSecs == 0 {
		maxLifetimeSecs = 3600
	}
	intervalSecs := positiveIntEnv("UBAG_JOB_REAPER_INTERVAL_SECONDS")
	if intervalSecs == 0 {
		intervalSecs = 60
	}
	return &executor.StaleJobReaper{
		Jobs:        jobs,
		Concurrency: concurrency,
		Notifier:    notifier,
		MaxLifetime: time.Duration(maxLifetimeSecs) * time.Second,
		Interval:    time.Duration(intervalSecs) * time.Second,
	}
}

func newWorkerConsumerFromEnv(dispatcher executor.Dispatcher, jobs jobstore.Store, notifier executor.TerminalJobNotifier, alertsMgr *alerts.Manager, conversationsMgr *conversations.Manager, concurrency *topology.ConcurrencyRegistry, topologyStore topology.Store, artifactStore artifacts.ArtifactStore, admission *topology.SQLTokenBackend) (*executor.WorkerConsumer, error) {
	pollInterval, err := durationFromMillisEnv("UBAG_WORKER_POLL_INTERVAL_MS", 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	// Opt-in idle backoff for the lease loop's fallback poll (default off: the
	// fixed UBAG_WORKER_POLL_INTERVAL_MS stays). The enqueue wake still fires
	// immediately for same-process enqueues.
	idlePollMax, err := idlePollMaxFromEnv()
	if err != nil {
		return nil, err
	}
	maxRuntime, err := durationFromMillisEnv("UBAG_WORKER_MAX_RUNTIME_MS", 30*time.Second)
	if err != nil {
		return nil, err
	}
	workerConcurrency, err := intFromEnv("UBAG_WORKER_CONCURRENCY", 1)
	if err != nil {
		return nil, err
	}
	if workerConcurrency > 32 {
		slog.Warn("ubag worker concurrency clamped", "requested", workerConcurrency, "applied", 32)
		workerConcurrency = 32
	}
	python, err := resolveExecutablePath(getenv("UBAG_WORKER_PYTHON", "python"))
	if err != nil {
		return nil, err
	}
	script, err := resolveWorkerScriptPath(getenv("UBAG_WORKER_SCRIPT", filepath.Join("apps", "worker", "run_mock_worker.py")))
	if err != nil {
		return nil, err
	}
	workerConcurrency = guardLiveWorkerConcurrency(workerConcurrency, script)
	queue, err := workerQueueFromEnv(dispatcher, maxRuntime, pollInterval)
	if err != nil {
		return nil, err
	}
	// Only the in-memory topology store accepts worker-reported topology
	// snapshots; SQLite/Postgres stores are populated by the worker out-of-band
	// and the type assertion intentionally yields a nil ingestor for them.
	topologyIngestor, _ := topologyStore.(topology.TopologyIngestor)
	// Login-state projection, by contrast, targets whichever store is actually
	// served: the in-memory, SQLite, and Postgres stores all implement
	// LoginStateWriter, so the live engine's real detect_login_state result
	// (session.authenticated / session.manual_action_required) is persisted onto
	// the SAME rows /v1/browser/contexts reads — no longer masked by the
	// deploy-time seed.
	loginStateWriter, _ := topologyStore.(topology.LoginStateWriter)
	runner, err := buildWorkerRunner(python, script, maxRuntime, artifactStore, jobs)
	if err != nil {
		return nil, err
	}
	// Per-job execution leases make duplicate deliveries harmless across
	// replicas; they need the shared admission database (memory mode relies on
	// the queue's own lease).
	var execLeases topology.TokenBackend
	if admission != nil {
		execLeases = admission
	}
	return &executor.WorkerConsumer{
		ExecLeases:       execLeases,
		Queue:            queue,
		Jobs:             jobs,
		TerminalNotifier: notifier,
		Alerts:           alertsMgr,
		Conversations:    conversationsMgr,
		Concurrency:      concurrency,
		Topology:         topologyIngestor,
		LoginState:       loginStateWriter,
		PollInterval:     pollInterval,
		IdlePollMax:      idlePollMax,
		PoolSize:         workerConcurrency,
		Runner:           runner,
	}, nil
}

func webhookWorkerEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_WEBHOOK_WORKER_ENABLED")))
	return value == "1" || value == "true" || value == "yes"
}

func newWebhookWorkerFromEnv(store webhooks.OutboxStore, policy webhooks.URLPolicy, breakerReg *resilience.Registry) (*webhooks.DeliveryWorker, error) {
	pollInterval, err := durationFromMillisEnv("UBAG_WEBHOOK_POLL_INTERVAL_MS", time.Second)
	if err != nil {
		return nil, err
	}
	leaseFor, err := durationFromMillisEnv("UBAG_WEBHOOK_LEASE_MS", 30*time.Second)
	if err != nil {
		return nil, err
	}
	requestTimeout, err := durationFromMillisEnv("UBAG_WEBHOOK_REQUEST_TIMEOUT_MS", 10*time.Second)
	if err != nil {
		return nil, err
	}
	baseDelay, err := durationFromMillisEnv("UBAG_WEBHOOK_RETRY_BASE_MS", time.Second)
	if err != nil {
		return nil, err
	}
	maxDelay, err := durationFromMillisEnv("UBAG_WEBHOOK_RETRY_MAX_MS", 5*time.Minute)
	if err != nil {
		return nil, err
	}
	maxAttempts, err := intFromEnv("UBAG_WEBHOOK_MAX_ATTEMPTS", 8)
	if err != nil {
		return nil, err
	}
	batchSize, err := intFromEnv("UBAG_WEBHOOK_BATCH_SIZE", 10)
	if err != nil {
		return nil, err
	}
	client := webhooks.NewHTTPClient(requestTimeout, policy)
	return &webhooks.DeliveryWorker{
		Store: store,
		Sender: webhooks.HTTPSender{
			Client:           client,
			SecretResolver:   webhooks.NewEnvSecretResolver(os.Getenv("UBAG_WEBHOOK_SECRET"), getenv("UBAG_WEBHOOK_SECRET_ENV_PREFIX", "UBAG_WEBHOOK_SECRET_")),
			URLPolicy:        policy,
			APIVersion:       getenv("UBAG_API_VERSION", httpapi.DefaultAPIVersion),
			MaxResponseBytes: int64(positiveIntEnv("UBAG_WEBHOOK_MAX_RESPONSE_BYTES")),
			Breakers:         breakerReg,
		},
		WorkerID:     getenv("UBAG_WEBHOOK_WORKER_ID", "gateway-webhook-worker"),
		PollInterval: pollInterval,
		LeaseFor:     leaseFor,
		BatchSize:    batchSize,
		Breakers:     breakerReg,
		RetryPolicy: webhooks.RetryPolicy{
			MaxAttempts: maxAttempts,
			BaseDelay:   baseDelay,
			MaxDelay:    maxDelay,
			JitterRatio: 0.2,
		},
	}, nil
}

func newWebhookURLPolicyFromEnv() webhooks.URLPolicy {
	return webhooks.URLPolicy{
		AllowInsecureHTTP:  envBool("UBAG_WEBHOOK_ALLOW_INSECURE_HTTP"),
		AllowPrivateHosts:  envBool("UBAG_WEBHOOK_ALLOW_PRIVATE_HOSTS"),
		AllowAnyPublicHost: envBool("UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST"),
		AllowedHosts:       csvEnv("UBAG_WEBHOOK_ALLOWED_HOSTS"),
		Resolver:           net.DefaultResolver,
	}
}

func envBool(key string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return value == "1" || value == "true" || value == "yes"
}

// envBoolDefaultTrue is true unless the variable is explicitly 0/false/no/off.
func envBoolDefaultTrue(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

func csvEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	values := []string{}
	for _, value := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

func workerQueueFromEnv(dispatcher executor.Dispatcher, maxRuntime time.Duration, pollInterval time.Duration) (executor.WorkerQueue, error) {
	mode := strings.ToLower(strings.TrimSpace(getenv("UBAG_EXECUTOR_MODE", "noop")))
	switch mode {
	case "file":
		fileDispatcher, ok := dispatcher.(*executor.FileSpoolDispatcher)
		if !ok {
			return nil, fmt.Errorf("worker consumer requires file executor mode")
		}
		return executor.NewFileSpoolWorkerQueue(fileDispatcher), nil
	case "nats":
		url, streamName, subject := natsDispatcherConfigFromEnv()
		ackDefault := 30 * time.Second
		if maxRuntime+5*time.Second > ackDefault {
			ackDefault = maxRuntime + 5*time.Second
		}
		ackWait, err := durationFromMillisEnv("UBAG_NATS_WORKER_ACK_WAIT_MS", ackDefault)
		if err != nil {
			return nil, err
		}
		// Hard invariant, not operator discipline: an ack wait at or below the
		// worker's max runtime lets JetStream redeliver a message while the job is
		// STILL RUNNING. A second worker would then drive the SAME shared browser
		// profile — generating the report twice and risking interleaved/cross-patient
		// output. Refuse to start rather than allow duplicate in-flight execution.
		if ackWait <= maxRuntime {
			return nil, fmt.Errorf(
				"UBAG_NATS_WORKER_ACK_WAIT_MS (%s) must be greater than UBAG_WORKER_MAX_RUNTIME_MS (%s): a shorter ack wait causes JetStream to redeliver and duplicate still-running jobs",
				ackWait, maxRuntime)
		}
		nakDelay, err := durationFromMillisEnv("UBAG_NATS_WORKER_NAK_DELAY_MS", time.Second)
		if err != nil {
			return nil, err
		}
		fetchWait, err := durationFromMillisEnv("UBAG_NATS_WORKER_FETCH_WAIT_MS", pollInterval)
		if err != nil {
			return nil, err
		}
		maxDeliver, err := intFromEnv("UBAG_NATS_WORKER_MAX_DELIVER", 5)
		if err != nil {
			return nil, err
		}
		// Read region for worker subscription. UBAG_WORKER_ROAMING=1 widens to all regions.
		workerRegion := strings.TrimSpace(os.Getenv("UBAG_REGION"))
		if strings.TrimSpace(os.Getenv("UBAG_WORKER_ROAMING")) == "1" {
			workerRegion = "" // empty = all regions (no region filter segment)
		}
		return executor.NewNATSWorkerQueue(executor.NATSWorkerQueueConfig{
			URL:        url,
			StreamName: streamName,
			Subject:    subject,
			Durable:    getenv("UBAG_NATS_WORKER_DURABLE", "ubag-worker"),
			AckWait:    ackWait,
			NakDelay:   nakDelay,
			FetchWait:  fetchWait,
			MaxDeliver: maxDeliver,
			Region:     workerRegion,
		})
	default:
		return nil, fmt.Errorf("worker consumer requires UBAG_EXECUTOR_MODE=file or nats; got %q", mode)
	}
}

func intFromEnv(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

// idlePollMaxFromEnv reads UBAG_WORKER_IDLE_POLL_MAX_MS; unset means 0 (off).
func idlePollMaxFromEnv() (time.Duration, error) {
	if strings.TrimSpace(os.Getenv("UBAG_WORKER_IDLE_POLL_MAX_MS")) == "" {
		return 0, nil
	}
	return durationFromMillisEnv("UBAG_WORKER_IDLE_POLL_MAX_MS", 0)
}

func durationFromMillisEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer number of milliseconds", key)
	}
	return time.Duration(value) * time.Millisecond, nil
}

func resolveExecutablePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("worker executable path is empty")
	}
	if filepath.IsAbs(value) {
		if _, err := os.Stat(value); err != nil {
			return "", fmt.Errorf("worker executable %q is not accessible: %w", value, err)
		}
		return value, nil
	}
	resolved, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("worker executable %q was not found on PATH: %w", value, err)
	}
	return resolved, nil
}

func resolveWorkerScriptPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("worker script path is empty")
	}
	if !filepath.IsAbs(value) {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", err
		}
		value = absolute
	}
	if filepath.Ext(value) != ".py" {
		return "", fmt.Errorf("worker script %q must be a Python file", value)
	}
	info, err := os.Stat(value)
	if err != nil {
		return "", fmt.Errorf("worker script %q is not accessible: %w", value, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("worker script %q is a directory", value)
	}
	return value, nil
}

// Voice wiring: UBAG_VOICE_STORE selects the shared session store
// ("memory" default, "sqlite" single-node file, "postgres" shared across
// replicas reusing the gateway's pool). The media hub dials the browser
// container's audio relay over TCP (UBAG_VOICE_AUDIO_RELAY_ADDR).
const (
	voiceSweepInterval     = 30 * time.Second
	voiceReconcileInterval = 2 * time.Second
	admissionSweepInterval = 30 * time.Second
)

func newVoiceComponentsFromEnv(ctx context.Context, storeKind string, db *sql.DB, topo topology.Store) (voice.Store, httpapi.MediaNegotiator, func(), error) {
	mode := strings.ToLower(strings.TrimSpace(getenv("UBAG_VOICE_STORE", "memory")))
	if mode == "disabled" || mode == "off" {
		return nil, nil, func() {}, nil
	}
	switch mode {
	case "", "memory", "in_memory":
		if storeKind != "memory" && storeKind != "" {
			slog.Warn("UBAG_VOICE_STORE is memory while the gateway store is SQL-backed: voice sessions are per-process and lost on restart; set UBAG_VOICE_STORE to follow UBAG_GATEWAY_STORE before enabling live voice", "gateway_store", storeKind)
		}
		store := voice.NewMemoryStore()
		return store, newVoiceMediaHub(store, topo), func() {}, nil
	case "postgres", "postgresql":
		if storeKind != "postgres" || db == nil {
			return nil, nil, func() {}, fmt.Errorf("UBAG_VOICE_STORE=postgres requires UBAG_GATEWAY_STORE=postgres")
		}
		store := voice.NewPostgresStore(db)
		return store, newVoiceMediaHub(store, topo), func() {}, nil
	case "sqlite", "sqlite3":
		dsn := strings.TrimSpace(getenv("UBAG_VOICE_SQLITE_DSN", voiceSQLiteDSNDefault))
		if strings.HasPrefix(dsn, "~") {
			if home, homeErr := os.UserHomeDir(); homeErr == nil {
				dsn = filepath.Join(home, dsn[1:])
			}
		}
		dbHandle, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, nil, func() {}, err
		}
		dbHandle.SetMaxOpenConns(1)
		if err := dbHandle.PingContext(ctx); err != nil {
			_ = dbHandle.Close()
			return nil, nil, func() {}, err
		}
		store := voice.NewSQLiteStore(dbHandle)
		if err := store.Ready(ctx); err != nil {
			_ = dbHandle.Close()
			return nil, nil, func() {}, err
		}
		return store, newVoiceMediaHub(store, topo), func() { _ = dbHandle.Close() }, nil
	default:
		return nil, nil, func() {}, fmt.Errorf("unsupported UBAG_VOICE_STORE %q", mode)
	}
}

const voiceSQLiteDSNDefault = "~/.ubag/voice.db"

// newVoiceMediaHub builds the media hub and binds its lifecycle callbacks to
// the shared store: a media path that ends on its own terminates the session
// record (so its leases free immediately), and a client-side mute is
// persisted.
func newVoiceMediaHub(store voice.Store, topo topology.Store) *voice.MediaHub {
	secret := []byte(strings.TrimSpace(os.Getenv("UBAG_VOICE_RELAY_SECRET")))
	if len(secret) == 0 {
		slog.Warn("UBAG_VOICE_RELAY_SECRET is not set: the audio relay refuses unauthenticated sessions, so live voice media is unavailable")
	}
	hub := &voice.MediaHub{
		Dialer: &voice.TCPRelayDialer{Address: voiceRelayResolver(topo), Secret: secret},
		ICE:    voiceICEConfigFromEnv(),
	}
	hub.OnClosed = func(s voice.Session, reason string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Terminate(ctx, s.TenantID, s.ID, time.Now().UTC(), reason); err != nil {
			slog.Error("voice session terminate after media end failed", "session_id", s.ID, "error", err)
		}
	}
	hub.OnMute = func(s voice.Session, muted bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.SetMuted(ctx, s.TenantID, s.ID, muted, time.Now().UTC()); err != nil {
			slog.Warn("voice mute persist failed", "session_id", s.ID, "error", err)
		}
	}
	return hub
}

// voiceICEConfigFromEnv reads the NAT-traversal deployment settings:
//
//	UBAG_VOICE_STUN_URLS, UBAG_VOICE_TURN_URLS  comma-separated ICE URLs
//	UBAG_VOICE_TURN_SECRET                      coturn static-auth-secret
//	UBAG_VOICE_NAT_1TO1_IP                      public address of the gateway
//	UBAG_VOICE_MEDIA_PORT_MIN / _MAX            bounded UDP range to publish
//	UBAG_VOICE_SERVER_VIA_TURN=1                gateway allocates a relay too
func voiceICEConfigFromEnv() *voice.ICEConfig {
	split := func(key string) []string {
		var out []string
		for _, part := range strings.Split(os.Getenv(key), ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	}
	port := func(key string) uint16 {
		n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
		if err != nil || n < 1 || n > 65535 {
			return 0
		}
		return uint16(n)
	}
	cfg := &voice.ICEConfig{
		STUNURLs:      split("UBAG_VOICE_STUN_URLS"),
		TURNURLs:      split("UBAG_VOICE_TURN_URLS"),
		TURNSecret:    os.Getenv("UBAG_VOICE_TURN_SECRET"),
		NAT1To1IP:     strings.TrimSpace(os.Getenv("UBAG_VOICE_NAT_1TO1_IP")),
		PortMin:       port("UBAG_VOICE_MEDIA_PORT_MIN"),
		PortMax:       port("UBAG_VOICE_MEDIA_PORT_MAX"),
		ServerViaTURN: envBool("UBAG_VOICE_SERVER_VIA_TURN"),
	}
	if (cfg.PortMin == 0) != (cfg.PortMax == 0) || cfg.PortMin > cfg.PortMax {
		slog.Warn("ignoring UBAG_VOICE_MEDIA_PORT_MIN/MAX: both must be set and MIN <= MAX")
		cfg.PortMin, cfg.PortMax = 0, 0
	}
	if len(cfg.TURNURLs) > 0 && cfg.TURNSecret == "" {
		slog.Warn("UBAG_VOICE_TURN_URLS is set without UBAG_VOICE_TURN_SECRET: TURN is not offered to clients")
	}
	return cfg
}

// voiceRelayResolver maps a session's browser environment to its relay.
// Order: the explicit UBAG_VOICE_AUDIO_RELAY_MAP ("instance=host:port,..."),
// then the instance's own registered host (RemoteEndpoint host + the relay
// port) so different environments resolve to different relays, then the
// single-environment UBAG_VOICE_AUDIO_RELAY_ADDR. Anything else fails closed;
// the relay itself additionally admits one authenticated session at a time.
func voiceRelayResolver(topo topology.Store) func(voice.Session) (string, error) {
	explicit := map[string]string{}
	for _, pair := range strings.Split(os.Getenv("UBAG_VOICE_AUDIO_RELAY_MAP"), ",") {
		if instance, addr, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && instance != "" && addr != "" {
			explicit[strings.TrimSpace(instance)] = strings.TrimSpace(addr)
		}
	}
	port := strings.TrimSpace(getenv("UBAG_VOICE_AUDIO_RELAY_PORT", "9099"))
	legacy := strings.TrimSpace(os.Getenv("UBAG_VOICE_AUDIO_RELAY_ADDR"))
	return func(session voice.Session) (string, error) {
		if addr := explicit[session.InstanceRef]; addr != "" {
			return addr, nil
		}
		if topo != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			instances, err := topo.ListInstances(ctx, topology.InstanceFilter{TenantID: session.TenantID, Limit: 100})
			if err == nil {
				for _, instance := range instances {
					if instance.InstanceID != session.InstanceRef || instance.RemoteEndpoint == "" {
						continue
					}
					if u, err := url.Parse(instance.RemoteEndpoint); err == nil && u.Hostname() != "" {
						return net.JoinHostPort(u.Hostname(), port), nil
					}
				}
			}
		}
		if legacy != "" {
			return legacy, nil
		}
		return "", voice.ErrRelayUnavailable
	}
}

// reconcileVoiceMedia ends any media connection whose session is no longer
// live in the shared store — terminated or swept by ANY replica, or gone — so
// stale audio can never survive resource reassignment even when the replica
// that owns the connection did not perform the termination. It also mirrors
// the stored Muted flag onto the live media path. Store errors keep media
// alive (fail open) unless UBAG_VOICE_RECONCILER_FAIL_CLOSED is set, in which
// case a session whose store lookups fail voiceReconcileMaxStoreErrors times
// in a row is disconnected.
func reconcileVoiceMedia(ctx context.Context, hub voiceMediaPeer, store voice.Store) {
	r := &voiceReconciler{hub: hub, store: store, failClosed: envBool("UBAG_VOICE_RECONCILER_FAIL_CLOSED"),
		errs: map[string]int{}, muted: map[string]bool{}}
	ticker := time.NewTicker(voiceReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.step(ctx, time.Now().UTC())
		}
	}
}

// voiceReconcileMaxStoreErrors is the number of consecutive store errors for
// one session after which a fail-closed reconciler stops that session's media
// (about 10s at voiceReconcileInterval).
const voiceReconcileMaxStoreErrors = 5

// voiceMediaPeer is the slice of *voice.MediaHub the reconciler drives.
type voiceMediaPeer interface {
	Sessions() []voice.Session
	Disconnect(sessionID string)
	SetMuted(sessionID string, muted bool) bool
}

// voiceReconciler compares this replica's media paths with the shared store.
type voiceReconciler struct {
	hub        voiceMediaPeer
	store      voice.Store
	failClosed bool // UBAG_VOICE_RECONCILER_FAIL_CLOSED
	errs       map[string]int
	muted      map[string]bool // mute state last applied to the hub
}

func (r *voiceReconciler) step(ctx context.Context, now time.Time) {
	live := map[string]bool{}
	for _, session := range r.hub.Sessions() {
		live[session.ID] = true
		current, found, err := r.store.Get(ctx, session.TenantID, session.ID)
		if err != nil {
			// Store trouble is not evidence the session ended: fail open
			// unless the operator opted into fail-closed.
			r.errs[session.ID]++
			if r.failClosed && r.errs[session.ID] >= voiceReconcileMaxStoreErrors {
				slog.Error("voice reconciler: store unavailable, disconnecting media", "session_id", session.ID, "consecutive_errors", r.errs[session.ID])
				r.hub.Disconnect(session.ID)
			}
			continue
		}
		delete(r.errs, session.ID)
		if !found || !current.Status.Active() || (!current.LeaseExpires.IsZero() && current.LeaseExpires.Before(now)) {
			r.hub.Disconnect(session.ID)
			continue
		}
		known, seen := r.muted[session.ID]
		if !seen {
			known = session.Muted
		}
		if current.Muted != known {
			r.hub.SetMuted(session.ID, current.Muted)
		}
		r.muted[session.ID] = current.Muted
	}
	for id := range r.errs {
		if !live[id] {
			delete(r.errs, id)
		}
	}
	for id := range r.muted {
		if !live[id] {
			delete(r.muted, id)
		}
	}
}

// envPositiveInt reads a positive integer env var; unset, invalid or
// non-positive values mean 0 (the limit is disabled).
func envPositiveInt(key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
