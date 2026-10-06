package helperauth

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

func loadCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("%s: no PEM block", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// deploy/mtls/gen-certs.sh --node must emit certificates this package accepts:
// one URI SAN, clientAuth, not a CA, at most 72h, chaining to the dev CA, and a
// pin file equal to SPKIHex. A second issue under the same CA (rotation) yields
// a different pin that works as the registry's next pin.
func TestDevCertScriptIssuesAcceptableNodeCerts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script; Windows uses gen-certs.ps1")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not available")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "deploy", "mtls", "gen-certs.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	run := func(args ...string) ([]byte, error) {
		return exec.Command(bash, append([]string{script}, args...)...).CombinedOutput()
	}
	for _, args := range [][]string{
		{"--node", "helper-dev", "--out", dir},
		{"--node", "helper-dev", "--label", "next", "--out", dir},
	} {
		if out, err := run(args...); err != nil {
			t.Fatalf("gen-certs.sh %v: %v\n%s", args, err, out)
		}
	}
	for _, args := range [][]string{
		{"--node", "helper-dev", "--node-days", "4", "--out", dir},
		{"--node", "bad id", "--out", dir},
		{"--node", "-leading-dash", "--out", dir},
		{"--node", "helper-dev", "--label", "a/b", "--out", dir},
	} {
		if out, err := run(args...); err == nil {
			t.Fatalf("gen-certs.sh %v succeeded, want refusal\n%s", args, out)
		}
	}

	ca, err := LoadCAPool(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	var pins []string
	var certs []*x509.Certificate
	for _, base := range []string{"node-helper-dev", "node-helper-dev.next"} {
		cert := loadCert(t, filepath.Join(dir, base+".crt"))
		if _, err := cert.Verify(x509.VerifyOptions{Roots: ca, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			t.Fatalf("%s does not chain to the dev CA: %v", base, err)
		}
		if got := cert.NotAfter.Sub(cert.NotBefore); got > MaxCertLifetime {
			t.Fatalf("%s lifetime %v exceeds 72h", base, got)
		}
		pin, err := os.ReadFile(filepath.Join(dir, base+".spki.sha256"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(pin)); got != SPKIHex(cert) {
			t.Fatalf("%s pin file %q != SPKI of the certificate %q", base, got, SPKIHex(cert))
		}
		if _, bad := leafIdentity(cert); bad != "" {
			t.Fatalf("%s rejected as %q", base, bad)
		}
		pins, certs = append(pins, SPKIHex(cert)), append(certs, cert)
	}
	if pins[0] == pins[1] {
		t.Fatal("rotation reused the key: identical pins")
	}

	// Registry flow: current accepted, next accepted during overlap, old pin
	// rejected once the next pin is promoted.
	store := nodes.NewMemoryStore()
	ctx := context.Background()
	entry := nodes.RegistryEntry{NodeID: "helper-dev", URISAN: nodes.NodeURISAN("helper-dev"), SPKICurrent: pins[0], SPKINext: pins[1]}
	if err := store.PutRegistry(ctx, entry, time.Now()); err != nil {
		t.Fatal(err)
	}
	a := &Authenticator{Registry: store}
	for i, c := range certs {
		if _, err := a.Verify(ctx, c); err != nil {
			t.Fatalf("cert %d rejected during overlap: %v", i, err)
		}
	}
	if err := store.PromoteSPKI(ctx, "helper-dev", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Verify(ctx, certs[0]); err == nil {
		t.Fatal("old certificate accepted after promotion")
	}
	if _, err := a.Verify(ctx, certs[1]); err != nil {
		t.Fatalf("new certificate rejected after promotion: %v", err)
	}
}
