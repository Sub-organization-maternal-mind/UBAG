package helperclient

// Helper isolation acceptance against CONTAINERISED helpers (perf-fleet P7.3).
//
// Both tests are skipped unless the manually dispatched workflow
// .github/workflows/helper-isolation.yml (or an operator following it) provides
// the fixtures: two helper containers built from deploy/helper/Dockerfile on a
// private Docker network, certificates issued by a throwaway CA, and this
// package's tests as the primary. Nothing here runs in the normal test suites
// and none of it has been run by its author: there is no Docker on the
// development machine.
//
//	UBAG_ISOLATION_WRITE_FIXTURES=<dir>   TestIsolationWriteFixtures writes ca.pem, the primary's
//	                                      certificate and one directory per node (node.crt, node.key)
//	UBAG_ISOLATION_FIXTURES=<dir>         TestIsolationOnContainerisedHelpers dials the nodes in
//	                                      <dir>/fixtures.json
//	UBAG_ISOLATION_ADAPTERS_DIR           adapter registry the containers carry (default: the repo's)

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

const (
	containerPrimaryID = "primary-iso"
	// containerWorkload is what the containers are started with as
	// UBAG_HELPER_WORKLOAD_VERSION.
	containerWorkload = "isolation"
)

// containerNodes are the two helpers of the workflow: node id and fixed address
// on its private network. Capacity is helperMaxAttempts each.
var containerNodes = []struct{ ID, Addr string }{
	{"iso-a", "172.30.0.11:7443"},
	{"iso-b", "172.30.0.12:7443"},
}

type containerFixtures struct {
	Primary string          `json:"primary"`
	Nodes   []containerNode `json:"nodes"`
}

type containerNode struct {
	NodeID   string `json:"node_id"`
	Endpoint string `json:"endpoint"`
	SPKI     string `json:"spki_sha256"`
}

func TestIsolationWriteFixtures(t *testing.T) {
	dir := os.Getenv("UBAG_ISOLATION_WRITE_FIXTURES")
	if dir == "" {
		t.Skip("UBAG_ISOLATION_WRITE_FIXTURES is not set")
	}
	// These are throwaway keys for a CI run, valid for a day, readable by the
	// containers' non-root user. They are never committed.
	ca := authtest.NewCA(t)
	long := time.Now().Add(48 * time.Hour)
	fx := containerFixtures{Primary: containerPrimaryID}
	write := func(path string, data []byte) {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "ca.pem"), ca.PEM)
	primary := ca.Issue(t, authtest.Spec{NodeID: containerPrimaryID, URIs: []string{helper.PrimaryURISAN(containerPrimaryID)}, NotAfter: long})
	write(filepath.Join(dir, "primary.crt"), primary.CertPEM)
	write(filepath.Join(dir, "primary.key"), primary.KeyPEM)
	for _, n := range containerNodes {
		leaf := ca.Issue(t, authtest.Spec{NodeID: n.ID, NotAfter: long})
		nd := filepath.Join(dir, n.ID)
		if err := os.MkdirAll(nd, 0o755); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(nd, "ca.pem"), ca.PEM)
		write(filepath.Join(nd, "node.crt"), leaf.CertPEM)
		write(filepath.Join(nd, "node.key"), leaf.KeyPEM)
		fx.Nodes = append(fx.Nodes, containerNode{NodeID: n.ID, Endpoint: n.Addr, SPKI: leaf.SPKI})
	}
	raw, _ := json.MarshalIndent(fx, "", "  ")
	write(filepath.Join(dir, "fixtures.json"), raw)
}

func TestIsolationOnContainerisedHelpers(t *testing.T) {
	dir := os.Getenv("UBAG_ISOLATION_FIXTURES")
	if dir == "" {
		t.Skip("UBAG_ISOLATION_FIXTURES is not set (needs two helper containers; see isolation_containers_test.go)")
	}
	var fx containerFixtures
	raw, err := os.ReadFile(filepath.Join(dir, "fixtures.json"))
	if err != nil || json.Unmarshal(raw, &fx) != nil || len(fx.Nodes) < 2 {
		t.Fatalf("fixtures.json: %v", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("ca.pem holds no certificate")
	}
	clientCert, err := tls.LoadX509KeyPair(filepath.Join(dir, "primary.crt"), filepath.Join(dir, "primary.key"))
	if err != nil {
		t.Fatal(err)
	}
	adapters := os.Getenv("UBAG_ISOLATION_ADAPTERS_DIR")
	if adapters == "" {
		adapters = filepath.Join("..", "..", "..", "..", "adapters")
	}
	digest, err := helper.RegistryDigest(adapters)
	if err != nil {
		t.Fatal(err)
	}

	registry := nodes.NewMemoryStore()
	for _, n := range fx.Nodes {
		if err := registry.PutRegistry(context.Background(), nodes.RegistryEntry{
			NodeID: n.NodeID, URISAN: nodes.NodeURISAN(n.NodeID), SPKICurrent: n.SPKI,
		}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	dialer, err := New(Config{
		CAs: pool, Registry: registry,
		ClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &clientCert, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	// One store for the whole run: job ids keep counting, so no two workloads ever
	// share a job id (a helper keys its attempt records by job).
	f := &isoFleet{t: t, store: jobstore.NewMemoryStore(), markers: map[string]string{}, tenants: map[string]string{}}
	for _, node := range fx.Nodes {
		f.addNode(t, &isoNode{nodeID: node.NodeID, endpoint: node.Endpoint}, digest, containerWorkload, dialer, func(c *executor.RemoteConfig) {
			c.LeaseTTL, c.RenewEvery = 30*time.Second, 5*time.Second
		})
	}

	// Two real helpers hold at most helperMaxAttempts each.
	capacity := helperMaxAttempts * len(fx.Nodes)
	for _, n := range isoLadder() {
		if n > capacity {
			t.Logf("%d workloads need more than the %d slots of %d containers: not run", n, capacity, len(fx.Nodes))
			continue
		}
		t.Run(fmt.Sprintf("%d_workloads", n), func(t *testing.T) {
			f.t = t
			envs := make([]executor.DispatchEnvelope, n)
			ids := make([]string, n)
			for k := range n {
				job, env := f.job(tenantOf[k%2], "")
				envs[k], ids[k] = env, job.ID
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
			defer cancel()
			for k, err := range f.runAll(ctx, envs, func(k int) int { return k % len(f.nodes) }) {
				if err != nil {
					t.Fatalf("job %d: Run = %v", k, err)
				}
			}
			f.assertIsolated(t, ids, 1)
		})
	}
}
