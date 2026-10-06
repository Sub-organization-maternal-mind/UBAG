package helper

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

func TestCheckEnvIsolationRefusesPrimaryOnlyConfiguration(t *testing.T) {
	const secretValue = "s3cr3t-value-must-never-be-echoed"
	for _, name := range []string{
		"UBAG_APP_SECRET", "UBAG_MASTER_KEK_HEX", "UBAG_DATABASE_URL", "UBAG_POSTGRES_DSN", "UBAG_SQLITE_DSN",
		"UBAG_REMOTE_BROWSER_ENDPOINT", "UBAG_NOVNC_BASE_URL", "UBAG_FLEET_MANAGER_URL", "DATABASE_URL", "PGPASSWORD",
		"UBAG_MINIO_ACCESS_KEY", "UBAG_MINIO_SECRET_KEY", "UBAG_GARAGE_SECRET_KEY", "UBAG_NATS_URL",
		"UBAG_DATABASE_MAX_OPEN_CONNS", "UBAG_APP_JWT_PUBLIC_KEY", "UBAG_WEBHOOK_SECRET", "UBAG_WEBHOOK_SECRET_ACME",
		"UBAG_ANTIGRAVITY_ACCOUNT_1_API_KEY", "UBAG_ANTIGRAVITY_ENABLED", "UBAG_FLEET_POLL_SECONDS",
		"UBAG_VOICE_RELAY_SECRET", "UBAG_VOICE_TURN_SECRET", "UBAG_ALERT_SMTP_PASSWORD", "UBAG_SOME_NEW_API_TOKEN",
		"UBAG_SOME_NEW_PRIVATE_KEY",
	} {
		err := CheckEnvIsolation([]string{"PATH=/bin", name + "=" + secretValue})
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s must be refused, got %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), secretValue) {
			t.Errorf("%s: the error must name the variable, never its value: %v", name, err)
		}
	}
	for _, ok := range []string{
		"UBAG_APP_SECRET=",                // set but empty (compose-style) is fine
		"UBAG_HELPER_TLS_KEY_FILE=/k.pem", // the helper's own settings are exempt
		"UBAG_HELPER_CA_FILE=/ca.pem",
		"UBAG_WORKER_STRICT_SUBMIT=true",
		"UBAG_BROWSER_HEADED=true",
		"HOME=/root",
		"GITHUB_TOKEN=x", // not a UBAG variable
	} {
		if err := CheckEnvIsolation([]string{ok}); err != nil {
			t.Errorf("%s must be allowed: %v", ok, err)
		}
	}
	// Several offenders are reported together, sorted.
	err := CheckEnvIsolation([]string{"UBAG_NATS_URL=x", "UBAG_APP_SECRET=y"})
	if err == nil || strings.Index(err.Error(), "UBAG_APP_SECRET") > strings.Index(err.Error(), "UBAG_NATS_URL") {
		t.Errorf("offenders must be listed together and sorted: %v", err)
	}
}

func TestValidateListenAcceptsOnlyASpecificInterfaceAddress(t *testing.T) {
	for addr, ok := range map[string]bool{
		"10.8.0.2:8443":      true,
		"[fd00::2]:8443":     true,
		"127.0.0.1:8443":     true,
		":8443":              false,
		"0.0.0.0:8443":       false,
		"[::]:8443":          false,
		"helper.internal:80": false,
		"localhost:8443":     false,
		"10.8.0.2":           false,
		"10.8.0.2:0":         false,
		"10.8.0.2:70000":     false,
		"10.8.0.2:http":      false,
		"224.0.0.1:8443":     false,
		"":                   false,
	} {
		if err := ValidateListen(addr); (err == nil) != ok {
			t.Errorf("%q: want ok=%v, got %v", addr, ok, err)
		}
	}
}

func fakeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindChromeRefusesAHostWithoutItsOwnChrome(t *testing.T) {
	dir := t.TempDir()
	chrome := fakeExecutable(t, dir, "chrome-bin")
	none := func(string) (string, error) { return "", os.ErrNotExist }

	if got, err := FindChrome(chrome, none); err != nil || got != chrome {
		t.Fatalf("explicit path: %v %v", got, err)
	}
	if _, err := FindChrome(filepath.Join(dir, "missing"), none); err == nil {
		t.Error("an explicit path that does not exist must be refused (and must not fall back to PATH)")
	}
	if _, err := FindChrome(dir, none); err == nil {
		t.Error("a directory is not a browser")
	}
	if _, err := FindChrome("", none); err == nil || !strings.Contains(err.Error(), "no local Chrome") {
		t.Errorf("no Chrome anywhere must refuse to start: %v", err)
	}
	lookup := func(name string) (string, error) {
		if name == "chromium" {
			return chrome, nil
		}
		return "", os.ErrNotExist
	}
	if got, err := FindChrome("", lookup); err != nil || got != chrome {
		t.Fatalf("PATH lookup: %v %v", got, err)
	}
	if runtime.GOOS != "windows" {
		plain := filepath.Join(dir, "plain")
		if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := FindChrome(plain, none); err == nil {
			t.Error("a file without the execute bit must be refused")
		}
	}
}

// testHost lays out everything a Helper Node needs on disk: the CA bundle, its
// own certificate, an adapter registry and a Chrome.
type testHost struct {
	ca     *authtest.CA
	dir    string
	env    []string
	lookup func(string) (string, error)
	listen string
}

func newTestHost(t *testing.T, nodeCertID string) *testHost {
	t.Helper()
	dir := t.TempDir()
	ca := authtest.NewCA(t)
	node := ca.Issue(t, authtest.Spec{NodeID: nodeCertID, NotAfter: time.Now().Add(48 * time.Hour)})
	certFile, keyFile := node.WriteFiles(t, dir, "node")
	caFile := ca.WriteCA(t, dir, "ca.pem")
	adapters := writeRegistry(t, filepath.Join(dir, "adapters"), `{"b":2,"a":1}`)
	chrome := fakeExecutable(t, dir, "chrome-bin")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	return &testHost{
		ca: ca, dir: dir, listen: addr,
		lookup: func(string) (string, error) { return chrome, nil },
		env: []string{
			"PATH=/usr/bin",
			EnvListen + "=" + addr, EnvNodeID + "=helper-1", EnvCAFile + "=" + caFile,
			EnvTLSCertFile + "=" + certFile, EnvTLSKeyFile + "=" + keyFile,
			EnvPrimaryURISAN + "=" + PrimaryURISAN("primary-1"), EnvWorkloadVersion + "=w1",
			EnvAdaptersDir + "=" + adapters,
		},
	}
}

func (h *testHost) with(kv ...string) []string {
	out := append([]string(nil), h.env...)
	return append(out, kv...)
}

func TestSettingsFromEnv(t *testing.T) {
	h := newTestHost(t, "helper-1")
	st, err := SettingsFromEnv(h.env, h.lookup)
	if err != nil {
		t.Fatal(err)
	}
	if st.NodeID != "helper-1" || st.MaxAttempts != 1 || st.ChromeBin == "" || st.Listen != h.listen || st.PrimaryURISAN != PrimaryURISAN("primary-1") {
		t.Fatalf("settings: %+v", st)
	}
	st, err = SettingsFromEnv(h.with(EnvMaxAttempts+"=3", EnvPrimarySPKI+"="+strings.Repeat("A", 64)+", "+strings.Repeat("b", 64)), h.lookup)
	if err != nil || st.MaxAttempts != 3 || len(st.PrimarySPKI) != 2 || st.PrimarySPKI[0] != strings.Repeat("a", 64) {
		t.Fatalf("optional settings: %+v %v", st, err)
	}

	for name, tc := range map[string]struct {
		env  []string
		want string
	}{
		"primary-only secret present":  {h.with("UBAG_APP_SECRET=x"), "UBAG_APP_SECRET"},
		"a remote browser configured":  {h.with("UBAG_REMOTE_BROWSER_ENDPOINT=http://x"), "UBAG_REMOTE_BROWSER_ENDPOINT"},
		"wildcard listen":              {h.with(EnvListen + "=0.0.0.0:8443"), "WireGuard address"},
		"bad node id":                  {h.with(EnvNodeID + "=../x"), EnvNodeID},
		"primary identity of a node":   {h.with(EnvPrimaryURISAN + "=" + NodeURISAN("helper-2")), EnvPrimaryURISAN},
		"bad pin":                      {h.with(EnvPrimarySPKI + "=zz"), EnvPrimarySPKI},
		"too many attempts":            {h.with(EnvMaxAttempts + "=9"), EnvMaxAttempts},
		"missing CA file":              {h.with(EnvCAFile + "=/nonexistent/ca.pem"), EnvCAFile},
		"missing workload version":     {h.with(EnvWorkloadVersion + "="), EnvWorkloadVersion + " is required"},
		"missing everything":           {[]string{"PATH=/usr/bin"}, EnvListen + " is required"},
		"no local Chrome":              {h.env, "no local Chrome"},
		"bad workload version":         {h.with(EnvWorkloadVersion + "=a b"), EnvWorkloadVersion},
		"zero attempts":                {h.with(EnvMaxAttempts + "=0"), EnvMaxAttempts},
		"non-numeric attempts":         {h.with(EnvMaxAttempts + "=many"), EnvMaxAttempts},
		"explicit chrome that is gone": {h.with(EnvChromeBin + "=/nonexistent/chrome"), EnvChromeBin},
	} {
		lookup := h.lookup
		if name == "no local Chrome" {
			lookup = func(string) (string, error) { return "", os.ErrNotExist }
		}
		if _, err := SettingsFromEnv(tc.env, lookup); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error containing %q, got %v", name, tc.want, err)
		}
	}
	// Problems are reported together, not one per restart.
	_, err = SettingsFromEnv([]string{EnvListen + "=:1", EnvNodeID + "=!"}, func(string) (string, error) { return "", os.ErrNotExist })
	if err == nil || strings.Count(err.Error(), "\n") < 3 {
		t.Errorf("all problems should be listed: %v", err)
	}
}

func TestOpenRefusesACertificateThatDoesNotNameTheNode(t *testing.T) {
	h := newTestHost(t, "helper-9") // certificate says helper-9, settings say helper-1
	st, err := SettingsFromEnv(h.env, h.lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Open(nil); err == nil || !strings.Contains(err.Error(), NodeURISAN("helper-1")) {
		t.Fatalf("a certificate for another node must refuse to start: %v", err)
	}

	h = newTestHost(t, "helper-1")
	st, err = SettingsFromEnv(h.with(EnvAdaptersDir+"="+filepath.Join(h.dir, "no-adapters")), h.lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Open(nil); err == nil || !strings.Contains(err.Error(), "adapter registry") {
		t.Fatalf("a helper that cannot load its registry must refuse to start: %v", err)
	}
}

func TestOwnCertCheck(t *testing.T) {
	ca := authtest.NewCA(t)
	now := time.Now()
	check := OwnCertCheck("helper-1", nil)
	issue := func(mut func(*authtest.Spec)) error {
		s := authtest.Spec{NodeID: "helper-1", NotAfter: now.Add(time.Hour)}
		if mut != nil {
			mut(&s)
		}
		return check(ca.Issue(t, s).X509)
	}
	if err := issue(nil); err != nil {
		t.Fatalf("a conforming certificate: %v", err)
	}
	for name, mut := range map[string]func(*authtest.Spec){
		"another node":     func(s *authtest.Spec) { s.URIs = []string{NodeURISAN("helper-2")} },
		"two URI SANs":     func(s *authtest.Spec) { s.URIs = []string{NodeURISAN("helper-1"), NodeURISAN("helper-2")} },
		"a primary id":     func(s *authtest.Spec) { s.URIs = []string{PrimaryURISAN("helper-1")} },
		"no URI SAN":       func(s *authtest.Spec) { s.URIs = []string{} },
		"no serverAuth":    func(s *authtest.Spec) { s.EKUs = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} },
		"expired":          func(s *authtest.Spec) { s.NotBefore, s.NotAfter = now.Add(-2*time.Hour), now.Add(-time.Hour) },
		"longer than 72 h": func(s *authtest.Spec) { s.NotAfter = now.Add(100 * time.Hour) },
		"a CA":             func(s *authtest.Spec) { s.IsCA = true },
	} {
		if issue(mut) == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// The whole startup path over a real TCP listener with real mTLS: settings from
// the environment, files for every certificate, the registry digest in the
// handshake, attempts refused until a runner exists, a clean stop on cancel.
func TestOpenServesOverRealMTLSAndStopsCleanly(t *testing.T) {
	h := newTestHost(t, "helper-1")
	st, err := SettingsFromEnv(h.env, h.lookup)
	if err != nil {
		t.Fatal(err)
	}
	node, err := st.Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- node.Run(ctx) }()

	primary := h.ca.Issue(t, authtest.Spec{NodeID: "primary-1", URIs: []string{PrimaryURISAN("primary-1")}, NotAfter: time.Now().Add(time.Hour)})
	conn, err := grpc.NewClient(h.listen, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: h.ca.Pool, ServerName: "localhost", Certificates: []tls.Certificate{primary.TLS},
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := helperv1.NewHelperServiceClient(conn)
	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rcancel()
	hs, err := c.Handshake(rctx, &helperv1.HandshakeRequest{ProtocolVersion: ProtocolVersion})
	if err != nil {
		t.Fatal(err)
	}
	want, err := RegistryDigest(st.AdaptersDir)
	if err != nil || hs.GetRegistryDigest() != want || hs.GetNodeId() != "helper-1" || hs.GetWorkloadVersion() != "w1" {
		t.Fatalf("handshake %v, want digest %s (%v)", hs, want, err)
	}
	stream, err := c.RunAttempt(rctx, &helperv1.RunAttemptRequest{Fence: &helperv1.Fence{
		JobId: "job-1", AttemptId: "att_a", NodeId: "helper-1", LeaseGeneration: 1,
		InputFingerprint: "fp", WorkloadVersion: "w1",
	}, Provider: "mock", Target: "mock", CommandType: "submit", IdentityRef: "i", InputJson: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("with no runner wired the binary must refuse attempts")
	}
	if _, err := c.ReportCapacity(rctx, &helperv1.ReportCapacityRequest{}); err != nil {
		t.Fatalf("capacity must work: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not stop after its context ended")
	}
}
