package helper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
)

// Version is the build version the helper reports in Handshake; set it with
// -ldflags "-X github.com/ubag/ubag/apps/gateway/internal/helper.Version=...".
var Version = "dev"

// The helper's whole configuration surface. Nothing else is read, and none of
// it is a secret: certificates and keys are FILES the operator provisions.
const (
	EnvListen          = "UBAG_HELPER_LISTEN"              // WireGuard address: an IP literal and port, never a wildcard
	EnvNodeID          = "UBAG_HELPER_NODE_ID"             // must equal the URI SAN of the node certificate
	EnvCAFile          = "UBAG_HELPER_CA_FILE"             // PEM trust anchors: the fleet manager CA that issues the primary's certificate
	EnvTLSCertFile     = "UBAG_HELPER_TLS_CERT_FILE"       // this node's certificate (re-read when the file changes)
	EnvTLSKeyFile      = "UBAG_HELPER_TLS_KEY_FILE"        // its key
	EnvPrimaryURISAN   = "UBAG_HELPER_PRIMARY_URI_SAN"     // spiffe://ubag/primary/<id>
	EnvPrimarySPKI     = "UBAG_HELPER_PRIMARY_SPKI_SHA256" // optional, comma separated, at most two
	EnvWorkloadVersion = "UBAG_HELPER_WORKLOAD_VERSION"    // worker image/code version
	EnvAdaptersDir     = "UBAG_HELPER_ADAPTERS_DIR"        // adapter registry directory (default ./adapters)
	EnvChromeBin       = "UBAG_HELPER_CHROME_BIN"          // optional explicit Chrome path; else looked up on PATH
	EnvMaxAttempts     = "UBAG_HELPER_MAX_ATTEMPTS"        // concurrent attempts, 1..8 (default 1)
	EnvWorkerScript    = "UBAG_HELPER_WORKER_SCRIPT"       // path of apps/worker/run_worker_daemon.py (the warm worker the pool runs)
	EnvWorkerPython    = "UBAG_HELPER_WORKER_PYTHON"       // interpreter for it (default "python")
	EnvProfileRoot     = "UBAG_HELPER_PROFILE_ROOT"        // directory the node's browser profiles live under (the worker's UBAG_PROFILE_DIR)
	defaultAdaptersDir = "adapters"
	shutdownGrace      = 20 * time.Second
)

// A Helper Node holds no application secret, no database, queue or object-store
// credential and no browser endpoint of the primary: those belong to the
// primary. Starting with any of them present means the wrong environment was
// pointed at this host, so the helper refuses rather than carry them (the
// worker environment is a further allowlist, P4.12; this is the first gate).
var (
	forbiddenEnv = []string{
		"UBAG_APP_SECRET", "UBAG_MASTER_KEK_HEX", "UBAG_DATABASE_URL", "UBAG_POSTGRES_DSN", "UBAG_SQLITE_DSN",
		"UBAG_REMOTE_BROWSER_ENDPOINT", "UBAG_NOVNC_BASE_URL", "UBAG_FLEET_MANAGER_URL", "DATABASE_URL", "PGPASSWORD",
	}
	forbiddenEnvPrefixes = []string{
		"UBAG_MINIO_", "UBAG_GARAGE_", "UBAG_NATS_", "UBAG_DATABASE_", "UBAG_APP_JWT_", "UBAG_WEBHOOK_SECRET",
		"UBAG_ANTIGRAVITY_", "UBAG_FLEET_",
	}
	forbiddenEnvSuffixes = []string{
		"_SECRET", "_SECRET_KEY", "_ACCESS_KEY", "_API_KEY", "_PASSWORD", "_TOKEN", "_DSN", "_KEK_HEX",
		"_PRIVATE_KEY", "_PRIVATE_KEY_FILE",
	}
)

// CheckEnvIsolation fails when environ (KEY=VALUE entries) carries primary-only
// configuration with a non-empty value. The error names the variables, never
// their values.
func CheckEnvIsolation(environ []string) error {
	var bad []string
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		if v == "" || strings.HasPrefix(k, "UBAG_HELPER_") {
			continue
		}
		if slices.Contains(forbiddenEnv, k) ||
			slices.ContainsFunc(forbiddenEnvPrefixes, func(p string) bool { return strings.HasPrefix(k, p) }) ||
			(strings.HasPrefix(k, "UBAG_") && slices.ContainsFunc(forbiddenEnvSuffixes, func(s string) bool { return strings.HasSuffix(k, s) })) {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	slices.Sort(bad)
	return fmt.Errorf("refusing to start: the environment carries primary-only configuration (%s); a Helper Node holds no database, queue, object-store or application secrets and does not drive a remote browser", strings.Join(bad, ", "))
}

// ValidateListen accepts only host:port with an IP literal that can be one
// specific interface address (the WireGuard address): no hostname (resolution
// would decide where it binds), no wildcard.
func ValidateListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s must be host:port: %w", EnvListen, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("%s must name one interface IP (the WireGuard address), not a hostname or a wildcard", EnvListen)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("%s has an invalid port", EnvListen)
	}
	return nil
}

var errNoChrome = errors.New("no local Chrome found: install Chrome or Chromium on this host, or set " + EnvChromeBin)

// FindChrome resolves this host's own Chrome. A Helper Node drives a browser it
// runs itself; with none installed it must not start (a remote browser would
// put the provider session off the node the primary pinned).
func FindChrome(explicit string, lookPath func(string) (string, error)) (string, error) {
	if explicit != "" {
		if !executableFile(explicit) {
			return "", fmt.Errorf("%s=%q is not an executable file", EnvChromeBin, explicit)
		}
		return explicit, nil
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := lookPath(name); err == nil && executableFile(p) {
			return p, nil
		}
	}
	return "", errNoChrome
}

func executableFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || fi.Mode()&0o111 != 0
}

// Settings is the validated configuration of one ubag-helper process.
type Settings struct {
	Listen          string
	NodeID          string
	CAFile          string
	TLSCertFile     string
	TLSKeyFile      string
	PrimaryURISAN   string
	PrimarySPKI     []string
	WorkloadVersion string
	AdaptersDir     string
	ChromeBin       string
	MaxAttempts     int
	// WorkerScript, WorkerPython and ProfileRoot configure the worker pool the
	// binary wires in (internal/helper/runner). They are optional here so the
	// service core stays usable without a worker; cmd/ubag-helper requires the script.
	WorkerScript string
	WorkerPython string
	ProfileRoot  string
	// Voice is the helper-hosted voice configuration (UBAG_HELPER_VOICE, default off).
	Voice VoiceSettings
}

// SettingsFromEnv validates the process environment (KEY=VALUE entries, e.g.
// os.Environ()) and the host: isolation first, then every setting, the files and
// the local Chrome. Every problem is reported together.
func SettingsFromEnv(environ []string, lookPath func(string) (string, error)) (Settings, error) {
	if err := CheckEnvIsolation(environ); err != nil {
		return Settings{}, err
	}
	env := map[string]string{}
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = strings.TrimSpace(v)
	}
	st := Settings{
		Listen: env[EnvListen], NodeID: env[EnvNodeID], CAFile: env[EnvCAFile], TLSCertFile: env[EnvTLSCertFile],
		TLSKeyFile: env[EnvTLSKeyFile], PrimaryURISAN: env[EnvPrimaryURISAN], WorkloadVersion: env[EnvWorkloadVersion],
		AdaptersDir: env[EnvAdaptersDir], MaxAttempts: 1,
		WorkerScript: env[EnvWorkerScript], WorkerPython: env[EnvWorkerPython], ProfileRoot: env[EnvProfileRoot],
	}
	if st.AdaptersDir == "" {
		st.AdaptersDir = defaultAdaptersDir
	}
	var errs []error
	for _, r := range []struct{ name, v string }{
		{EnvListen, st.Listen}, {EnvNodeID, st.NodeID}, {EnvCAFile, st.CAFile}, {EnvTLSCertFile, st.TLSCertFile},
		{EnvTLSKeyFile, st.TLSKeyFile}, {EnvPrimaryURISAN, st.PrimaryURISAN}, {EnvWorkloadVersion, st.WorkloadVersion},
	} {
		if r.v == "" {
			errs = append(errs, fmt.Errorf("%s is required", r.name))
		}
	}
	if st.Listen != "" {
		if err := ValidateListen(st.Listen); err != nil {
			errs = append(errs, err)
		}
	}
	if st.NodeID != "" && !nodeIDRe.MatchString(st.NodeID) {
		errs = append(errs, fmt.Errorf("%s must match %s", EnvNodeID, nodeIDRe))
	}
	if st.WorkloadVersion != "" && !versionRe.MatchString(st.WorkloadVersion) {
		errs = append(errs, fmt.Errorf("%s must match %s", EnvWorkloadVersion, versionRe))
	}
	if raw := env[EnvPrimarySPKI]; raw != "" {
		st.PrimarySPKI = strings.Split(strings.ToLower(strings.ReplaceAll(raw, " ", "")), ",")
	}
	if st.PrimaryURISAN != "" {
		if _, err := NewPrimaryAuth(st.PrimaryURISAN, st.PrimarySPKI); err != nil {
			errs = append(errs, fmt.Errorf("%s / %s: %w", EnvPrimaryURISAN, EnvPrimarySPKI, err))
		}
	}
	voice, err := parseVoiceSettings(env)
	if err != nil {
		errs = append(errs, err)
	}
	st.Voice = voice
	if raw := env[EnvMaxAttempts]; raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxAttemptsCeiling {
			errs = append(errs, fmt.Errorf("%s must be 1..%d", EnvMaxAttempts, maxAttemptsCeiling))
		}
		st.MaxAttempts = n
	}
	for _, f := range []struct{ env, path string }{{EnvCAFile, st.CAFile}, {EnvTLSCertFile, st.TLSCertFile}, {EnvTLSKeyFile, st.TLSKeyFile}} {
		if f.path != "" {
			if fi, err := os.Stat(f.path); err != nil || !fi.Mode().IsRegular() {
				errs = append(errs, fmt.Errorf("%s: %q is not a readable file", f.env, f.path))
			}
		}
	}
	chrome, err := FindChrome(env[EnvChromeBin], lookPath)
	if err != nil {
		errs = append(errs, err)
	}
	st.ChromeBin = chrome
	if len(errs) > 0 {
		return Settings{}, errors.Join(errs...)
	}
	return st, nil
}

// Node is a configured Helper Node: the service, its mTLS gRPC server and the
// bound listener. Nothing serves until Run.
type Node struct {
	Settings       Settings
	RegistryDigest string
	Server         *Server
	GRPC           *grpc.Server
	Listener       net.Listener
}

// Open loads the registry digest, the CA, the node certificate (which must name
// st.NodeID) and binds the listener on the WireGuard address. runner may be nil
// (every attempt is then refused Unavailable, the state of the binary until
// P4.12 wires the worker pool). voice is the media endpoint of
// HelperVoiceService: it is required when st.Voice.Enabled and ignored
// otherwise (the service is then not registered at all).
func (st Settings) Open(runner Runner, voice VoiceMedia) (*Node, error) {
	if st.Voice.Enabled && voice == nil {
		return nil, errors.New(EnvVoice + " is on but this binary has no voice media endpoint (build it with -tags helpervoice)")
	}
	digest, err := RegistryDigest(st.AdaptersDir)
	if err != nil {
		return nil, fmt.Errorf("adapter registry: %w", err)
	}
	pool, err := LoadCAPool(st.CAFile)
	if err != nil {
		return nil, err
	}
	kp, err := NewKeyPair(st.TLSCertFile, st.TLSKeyFile, OwnCertCheck(st.NodeID, nil))
	if err != nil {
		return nil, err
	}
	auth, err := NewPrimaryAuth(st.PrimaryURISAN, st.PrimarySPKI)
	if err != nil {
		return nil, err
	}
	var lastLog atomic.Int64
	auth.OnReject = func(reason string) { // a peer on the WireGuard network can multiply these: at most one line a second
		now := time.Now().UnixNano()
		if prev := lastLog.Load(); now-prev > int64(time.Second) && lastLog.CompareAndSwap(prev, now) {
			slog.Warn("helper rejected a peer", "reason", reason)
		}
	}
	cfg := Config{
		NodeID: st.NodeID, HelperVersion: Version, WorkloadVersion: st.WorkloadVersion, RegistryDigest: digest,
		Runner: runner, Host: NewCgroupSampler("", ""), MaxAttempts: st.MaxAttempts,
	}
	if st.Voice.Enabled {
		cfg.Voice = &VoiceConfig{Media: voice, Environments: st.Voice.Environments}
	}
	srv, err := NewServer(cfg)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", st.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", st.Listen, err)
	}
	grpcSrv := auth.NewGRPCServer(auth.ServerTLSConfig(pool, kp.GetCertificate), srv)
	srv.RegisterVoice(grpcSrv) // a no-op unless voice is configured
	return &Node{Settings: st, RegistryDigest: digest, Server: srv, Listener: ln, GRPC: grpcSrv}, nil
}

// Run serves until ctx ends, then shuts down: new attempts are refused, running
// ones are killed (they end `failed`, stream_end_reason "shutdown") and the
// server stops, bounded by a grace period.
func (n *Node) Run(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- n.GRPC.Serve(n.Listener) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	_ = n.Server.Shutdown(sctx)
	stopped := make(chan struct{})
	go func() { n.GRPC.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-sctx.Done():
		n.GRPC.Stop()
	}
	return <-errc
}
