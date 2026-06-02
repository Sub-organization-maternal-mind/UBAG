package artifacts_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
)

// TestGarageStoreConstructor verifies that NewGarageArtifactStore returns a
// non-nil store and that the returned value satisfies the ArtifactStore
// interface. No live Garage node is required.
func TestGarageStoreConstructor(t *testing.T) {
	store, err := artifacts.NewGarageArtifactStore("localhost:3900", "k", "s", "", false, nil)
	if err != nil {
		t.Fatalf("NewGarageArtifactStore: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	// Compile-time check: *GarageArtifactStore must satisfy ArtifactStore.
	var _ artifacts.ArtifactStore = store
}

// TestNewGarageFromEnvMissingEndpoint verifies that
// NewGarageArtifactStoreFromEnv returns an error when UBAG_GARAGE_ENDPOINT is
// not set.
func TestNewGarageFromEnvMissingEndpoint(t *testing.T) {
	t.Setenv("UBAG_GARAGE_ENDPOINT", "")
	os.Unsetenv("UBAG_GARAGE_ENDPOINT") //nolint:errcheck
	_, err := artifacts.NewGarageArtifactStoreFromEnv(nil)
	if err == nil {
		t.Fatal("expected error when UBAG_GARAGE_ENDPOINT is missing; got nil")
	}
}

// TestReplicatingStorePutMirrorsToRemote verifies that PutArtifact writes to
// the home store immediately and to the remote store after Stop.
func TestReplicatingStorePutMirrorsToRemote(t *testing.T) {
	ctx := context.Background()
	home := artifacts.NewMemoryArtifactStore()
	remote := artifacts.NewMemoryArtifactStore()

	rs := artifacts.NewReplicatingStore(home, []artifacts.ArtifactStore{remote}, 16)
	rs.Start()

	data := []byte("hello garage replication")
	_, err := rs.PutArtifact(ctx, "job-rep-001", "report.txt", "text/plain", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	// Home store must have the artifact immediately.
	rc, _, err := home.GetArtifact(ctx, "job-rep-001", "report.txt")
	if err != nil {
		t.Fatalf("home.GetArtifact: %v", err)
	}
	rc.Close()

	// Stop drains the queue and waits for mirror writes to complete.
	rs.Stop(ctx)

	// Remote store must now have the artifact.
	rc2, rec, err := remote.GetArtifact(ctx, "job-rep-001", "report.txt")
	if err != nil {
		t.Fatalf("remote.GetArtifact after Stop: %v", err)
	}
	defer rc2.Close()

	got, _ := io.ReadAll(rc2)
	if !bytes.Equal(got, data) {
		t.Errorf("remote data = %q; want %q", got, data)
	}
	if rec.Key != "report.txt" {
		t.Errorf("remote rec.Key = %q; want report.txt", rec.Key)
	}
}

// TestReplicatingStoreReadDelegatesToHome verifies that GetArtifact reads from
// the home store only; the remote store is not consulted.
func TestReplicatingStoreReadDelegatesToHome(t *testing.T) {
	ctx := context.Background()
	home := artifacts.NewMemoryArtifactStore()
	remote := artifacts.NewMemoryArtifactStore()

	// Seed home directly.
	payload := []byte("home-only data")
	_, err := home.PutArtifact(ctx, "job-read-001", "data.bin", "application/octet-stream", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("home.PutArtifact: %v", err)
	}

	rs := artifacts.NewReplicatingStore(home, []artifacts.ArtifactStore{remote}, 16)
	// Do NOT call Start — we never put anything into the queue here.

	rc, _, err := rs.GetArtifact(ctx, "job-read-001", "data.bin")
	if err != nil {
		t.Fatalf("rs.GetArtifact: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Errorf("GetArtifact data = %q; want %q", got, payload)
	}

	// Remote should not have the artifact.
	if _, _, err := remote.GetArtifact(ctx, "job-read-001", "data.bin"); !artifacts.IsNotFound(err) {
		t.Errorf("remote.GetArtifact error = %v; want IsNotFound", err)
	}
}

// TestReplicatingStoreReadyDelegatesToHome verifies that Ready delegates to the
// home store and not the remote stores.
func TestReplicatingStoreReadyDelegatesToHome(t *testing.T) {
	ctx := context.Background()
	home := artifacts.NewMemoryArtifactStore()
	remote := artifacts.NewMemoryArtifactStore()

	rs := artifacts.NewReplicatingStore(home, []artifacts.ArtifactStore{remote}, 16)

	if err := rs.Ready(ctx); err != nil {
		t.Fatalf("rs.Ready: %v", err)
	}
}
