package serve

import (
	"context"
	"database/sql"
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
	"syscall"
	"time"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/ubag/ubag/apps/gateway/internal/alerts"
	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/grpcapi"
	"github.com/ubag/ubag/apps/gateway/internal/httpapi"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/obs"
	"github.com/ubag/ubag/apps/gateway/internal/profile"
	"github.com/ubag/ubag/apps/gateway/internal/ratelimit"
	"github.com/ubag/ubag/apps/gateway/internal/resilience"
	"github.com/ubag/ubag/apps/gateway/internal/responsecache"
	"github.com/ubag/ubag/apps/gateway/internal/scim"
	"github.com/ubag/ubag/apps/gateway/internal/session"
	"github.com/ubag/ubag/apps/gateway/internal/siem"
	"github.com/ubag/ubag/apps/gateway/internal/sqlitestore"
	"github.com/ubag/ubag/apps/gateway/internal/sso"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
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

// Run starts the gateway and blocks until the context is cancelled or a fatal
// error occurs.
func Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Initialise contract-conformant JSON logging (§18.1). Must come before any
	// slog calls so all downstream logs use the redacting handler.
	logger := obs.InitLogger(ctx, os.Stderr)
	slog.SetDefault(logger)

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
	dispatcher, err := newDispatcherFromEnv()
	if err != nil {
		return fmt.Errorf("invalid executor configuration: %w", err)
	}
	if closer, ok := dispatcher.(interface{ Close() }); ok {
		defer closer.Close()
	}
	breakerRegistry := resilience.NewRegistry(resilience.DefaultConfig())
	dispatcher = resilience.DispatcherMiddleware(dispatcher, breakerRegistry)
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
	webhookMaxAttempts, err := intFromEnv("UBAG_WEBHOOK_MAX_ATTEMPTS", 8)
	if err != nil {
		return fmt.Errorf("invalid webhook retry configuration: %w", err)
	}
	webhookOutbox := &webhooks.JobOutbox{
		Store:       webhookStore,
		URLPolicy:   webhookPolicy,
		MaxAttempts: webhookMaxAttempts,
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

	server := httpapi.NewServer(httpapi.Config{
		APIVersion:       getenv("UBAG_API_VERSION", httpapi.DefaultAPIVersion),
		Version:          getenv("UBAG_GATEWAY_VERSION", "0.0.0-dev"),
		BuildCommit:      getenv("UBAG_BUILD_COMMIT", "unknown"),
		AppSecret:        getenv("UBAG_APP_SECRET", ""),
		TenantID:         getenv("UBAG_TENANT_ID", ""),
		AppID:            getenv("UBAG_APP_ID", ""),
		ActorRole:        getenv("UBAG_ACTOR_ROLE", ""),
		Idempotency:      idempotencyStore,
		Jobs:             jobs,
		Executor:         dispatcher,
		Artifacts:        artifactStore,
		Webhooks:         webhookStore,
		WebhookURLPolicy: webhookPolicy,

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
		Alerts:            enterprise.alerts,
		Topology:          enterprise.topology,
		Concurrency:       enterprise.concurrency,
	})

	if workerConsumerEnabled() {
		consumer, err := newWorkerConsumerFromEnv(dispatcher, jobs, webhookOutbox, enterprise.alerts, enterprise.concurrency, enterprise.topology)
		if err != nil {
			return fmt.Errorf("invalid worker consumer configuration: %w", err)
		}
		if err := consumer.Ready(ctx); err != nil {
			return fmt.Errorf("worker consumer is not ready: %w", err)
		}
		if closer, ok := consumer.Queue.(interface{ Close() }); ok {
			defer closer.Close()
		}
		go func() {
			if err := consumer.Run(ctx); err != nil && err != context.Canceled {
				slog.Error("worker consumer stopped", "error", err)
			}
		}()
	}
	if webhookWorkerEnabled() {
		worker, err := newWebhookWorkerFromEnv(webhookStore, webhookPolicy, breakerRegistry)
		if err != nil {
			return fmt.Errorf("invalid webhook worker configuration: %w", err)
		}
		if err := worker.Ready(ctx); err != nil {
			return fmt.Errorf("webhook worker is not ready: %w", err)
		}
		go func() {
			if err := worker.Run(ctx); err != nil && err != context.Canceled {
				slog.Error("webhook worker stopped", "error", err)
			}
		}()
	}

	grpcServer := grpc.NewServer()
	ubagv1.RegisterJobServiceServer(grpcServer, grpcapi.NewServer(grpcapi.Config{
		APIVersion:  getenv("UBAG_API_VERSION", httpapi.DefaultAPIVersion),
		AppSecret:   getenv("UBAG_APP_SECRET", ""),
		TenantID:    getenv("UBAG_TENANT_ID", ""),
		AppID:       getenv("UBAG_APP_ID", ""),
		ActorRole:   getenv("UBAG_ACTOR_ROLE", ""),
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
	}

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
		grpcServer.GracefulStop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
		return executor.NewFileSpoolDispatcher(spoolDir), nil
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
	alerts           *alerts.Manager
	topology         topology.Store
	concurrency      *topology.ConcurrencyRegistry
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
	var rlStore ratelimit.Store
	switch {
	case storeKind == "sqlite" && db != nil:
		store, err := ratelimit.NewSQLiteStore(ctx, db)
		if err != nil {
			return enterpriseStores{}, fmt.Errorf("rate limit sqlite store: %w", err)
		}
		rlStore = store
	case storeKind == "postgres" && db != nil:
		store, err := ratelimit.NewPostgresStore(ctx, db)
		if err != nil {
			return enterpriseStores{}, fmt.Errorf("rate limit postgres store: %w", err)
		}
		rlStore = store
	default:
		rlStore = ratelimit.NewMemoryStore()
	}
	out.rateLimiter = ratelimit.New(rlStore, out.rateResolver.Default())

	// Response cache (gated by UBAG_CACHE_ENABLED, default off).
	cacheEnabled := envBool("UBAG_CACHE_ENABLED")
	cacheTTL, err := durationFromMillisEnv("UBAG_CACHE_TTL_MS", 5*time.Minute)
	if err != nil {
		return enterpriseStores{}, fmt.Errorf("invalid UBAG_CACHE_TTL_MS: %w", err)
	}
	var cacheStore responsecache.Store
	switch {
	case storeKind == "sqlite" && db != nil:
		sqliteCache := responsecache.NewSQLiteStore(db)
		if err := sqliteCache.EnsureSchema(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("response cache sqlite schema: %w", err)
		}
		cacheStore = sqliteCache
	case storeKind == "postgres" && db != nil:
		pgCache := responsecache.NewPostgresStore(db)
		if err := pgCache.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("response cache postgres store: %w", err)
		}
		cacheStore = pgCache
	default:
		cacheStore = responsecache.NewMemoryStore()
	}
	out.responseCache = responsecache.New(cacheStore, responsecache.Options{TTL: cacheTTL, Enabled: cacheEnabled})

	// Workflow orchestration store.
	switch {
	case storeKind == "sqlite" && db != nil:
		wfStore := workflow.NewSQLiteStore(db)
		if err := wfStore.Migrate(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("workflow sqlite migrate: %w", err)
		}
		out.workflows = wfStore
	case storeKind == "postgres" && db != nil:
		wfStore := workflow.NewPostgresStore(db)
		if err := wfStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("workflow postgres store: %w", err)
		}
		out.workflows = wfStore
	default:
		out.workflows = workflow.NewMemoryStore()
	}

	// SSO configuration store.
	switch {
	case storeKind == "sqlite" && db != nil:
		ssoStore := sso.NewSQLiteStore(db)
		if err := ssoStore.Migrate(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("sso sqlite migrate: %w", err)
		}
		out.sso = ssoStore
	case storeKind == "postgres" && db != nil:
		ssoStore := sso.NewPostgresStore(db)
		if err := ssoStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("sso postgres store: %w", err)
		}
		out.sso = ssoStore
	default:
		out.sso = sso.NewMemoryStore()
	}

	// SCIM provisioning store.
	switch {
	case storeKind == "sqlite" && db != nil:
		scimStore, err := scim.NewSQLiteStore(db)
		if err != nil {
			return enterpriseStores{}, fmt.Errorf("scim sqlite store: %w", err)
		}
		out.scim = scimStore
	case storeKind == "postgres" && db != nil:
		scimStore, err := scim.NewPostgresStore(db)
		if err != nil {
			return enterpriseStores{}, fmt.Errorf("scim postgres store: %w", err)
		}
		if err := scimStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("scim postgres store: %w", err)
		}
		out.scim = scimStore
	default:
		out.scim = scim.NewMemoryStore()
	}

	// SIEM sink configuration store.
	switch {
	case storeKind == "sqlite" && db != nil:
		siemStore := siem.NewSQLiteStore(db)
		if err := siemStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("siem sqlite schema: %w", err)
		}
		out.siemConfig = siemStore
	case storeKind == "postgres" && db != nil:
		siemStore := siem.NewPostgresStore(db)
		if err := siemStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("siem postgres schema: %w", err)
		}
		out.siemConfig = siemStore
	default:
		out.siemConfig = siem.NewMemoryStore()
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
	switch {
	case storeKind == "sqlite" && db != nil:
		secretStore := httpapi.NewSQLiteWebhookSecretStore(db)
		if err := secretStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("webhook secret sqlite schema: %w", err)
		}
		out.webhookSecrets = secretStore
	case storeKind == "postgres" && db != nil:
		secretStore := httpapi.NewPostgresWebhookSecretStore(db)
		if err := secretStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("webhook secret postgres schema: %w", err)
		}
		out.webhookSecrets = secretStore
	default:
		out.webhookSecrets = httpapi.NewMemoryWebhookSecretStore()
	}

	// Audit log store (Merkle-chained, per-tenant).
	switch {
	case storeKind == "sqlite" && db != nil:
		auditStore := audit.NewSQLiteStore(db)
		if err := auditStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("audit sqlite schema: %w", err)
		}
		out.audit = auditStore
	case storeKind == "postgres" && db != nil:
		auditStore := audit.NewPostgresStore(db)
		if err := auditStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("audit postgres schema: %w", err)
		}
		out.audit = auditStore
	default:
		out.audit = audit.NewMemoryStore()
	}

	// Server-side SSO session store.
	sessionTTL, err := durationFromMillisEnv("UBAG_SESSION_TTL_MS", time.Hour)
	if err != nil {
		return enterpriseStores{}, err
	}
	out.sessionTTL = sessionTTL
	switch {
	case storeKind == "sqlite" && db != nil:
		sessionStore := session.NewSQLiteStore(db)
		if err := sessionStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("session sqlite schema: %w", err)
		}
		out.sessions = sessionStore
	case storeKind == "postgres" && db != nil:
		sessionStore := session.NewPostgresStore(db)
		if err := sessionStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("session postgres schema: %w", err)
		}
		out.sessions = sessionStore
	default:
		out.sessions = session.NewMemoryStore()
	}

	// Human-in-the-loop manual-action alert store + notification sink.
	sink, summary := alerts.SinkFromEnv(slog.Default(), storeKind)
	var alertStore alerts.Store
	switch {
	case storeKind == "sqlite" && db != nil:
		sqliteAlerts := alerts.NewSQLiteStore(db)
		if err := sqliteAlerts.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("alerts sqlite schema: %w", err)
		}
		alertStore = sqliteAlerts
	case storeKind == "postgres" && db != nil:
		postgresAlerts := alerts.NewPostgresStore(db)
		if err := postgresAlerts.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("alerts postgres schema: %w", err)
		}
		alertStore = postgresAlerts
	default:
		alertStore = alerts.NewMemoryStore()
	}
	out.alerts = alerts.NewManager(alertStore, sink, slog.Default(), summary)

	// Read-only v2.1 browser topology store + adaptive-concurrency view.
	switch {
	case storeKind == "sqlite" && db != nil:
		topologyStore := topology.NewSQLiteStore(db)
		if err := topologyStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("browser topology sqlite schema: %w", err)
		}
		out.topology = topologyStore
	case storeKind == "postgres" && db != nil:
		topologyStore := topology.NewPostgresStore(db)
		if err := topologyStore.Ready(ctx); err != nil {
			return enterpriseStores{}, fmt.Errorf("browser topology postgres schema: %w", err)
		}
		out.topology = topologyStore
	default:
		out.topology = topology.NewMemoryStore()
	}
	// The concurrency registry is always available; it is populated by the
	// worker-event ingestion path and never mutated via HTTP.
	out.concurrency = topology.NewConcurrencyRegistry()

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

func configureDBPoolFromEnv(db *sql.DB) {
	if value := positiveIntEnv("UBAG_DATABASE_MAX_OPEN_CONNS"); value > 0 {
		db.SetMaxOpenConns(value)
	}
	if value := positiveIntEnv("UBAG_DATABASE_MAX_IDLE_CONNS"); value > 0 {
		db.SetMaxIdleConns(value)
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

func newWorkerConsumerFromEnv(dispatcher executor.Dispatcher, jobs jobstore.Store, notifier executor.TerminalJobNotifier, alertsMgr *alerts.Manager, concurrency *topology.ConcurrencyRegistry, topologyStore topology.Store) (*executor.WorkerConsumer, error) {
	pollInterval, err := durationFromMillisEnv("UBAG_WORKER_POLL_INTERVAL_MS", 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	maxRuntime, err := durationFromMillisEnv("UBAG_WORKER_MAX_RUNTIME_MS", 30*time.Second)
	if err != nil {
		return nil, err
	}
	python, err := resolveExecutablePath(getenv("UBAG_WORKER_PYTHON", "python"))
	if err != nil {
		return nil, err
	}
	script, err := resolveWorkerScriptPath(getenv("UBAG_WORKER_SCRIPT", filepath.Join("apps", "worker", "run_mock_worker.py")))
	if err != nil {
		return nil, err
	}
	queue, err := workerQueueFromEnv(dispatcher, maxRuntime, pollInterval)
	if err != nil {
		return nil, err
	}
	// Only the in-memory topology store accepts worker-reported topology
	// snapshots; SQLite/Postgres stores are populated by the worker out-of-band
	// and the type assertion intentionally yields a nil ingestor for them.
	topologyIngestor, _ := topologyStore.(topology.TopologyIngestor)
	return &executor.WorkerConsumer{
		Queue:            queue,
		Jobs:             jobs,
		TerminalNotifier: notifier,
		Alerts:           alertsMgr,
		Concurrency:      concurrency,
		Topology:         topologyIngestor,
		PollInterval:     pollInterval,
		Runner: executor.ProcessWorkerRunner{
			Python:     python,
			Script:     script,
			MaxRuntime: maxRuntime,
		},
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
		return executor.NewNATSWorkerQueue(executor.NATSWorkerQueueConfig{
			URL:        url,
			StreamName: streamName,
			Subject:    subject,
			Durable:    getenv("UBAG_NATS_WORKER_DURABLE", "ubag-worker"),
			AckWait:    ackWait,
			NakDelay:   nakDelay,
			FetchWait:  fetchWait,
			MaxDeliver: maxDeliver,
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
