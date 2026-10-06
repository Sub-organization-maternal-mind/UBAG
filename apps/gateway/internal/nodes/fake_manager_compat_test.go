package nodes

import (
	"context"
	"os"
	"testing"
)

// Skipped unless UBAG_FAKE_MANAGER_URL points at a running
// `node tools/fake-fleet-manager.mjs` (P4.20 rehearsal tool). Proves the fake's
// body is accepted by the real strict parser, including the ETag round trip.
func TestFakeFleetManagerBodyParsesWithTheRealSource(t *testing.T) {
	url := os.Getenv("UBAG_FAKE_MANAGER_URL")
	if url == "" {
		t.Skip("UBAG_FAKE_MANAGER_URL not set")
	}
	src, err := NewHTTPSource(url, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := src.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(snap.Allocations) != 1 || snap.ETag == "" {
		t.Fatalf("want one allocation and an etag, got %d %q", len(snap.Allocations), snap.ETag)
	}
	again, err := src.Fetch(context.Background(), snap.ETag)
	if err != nil || !again.NotModified {
		t.Fatalf("etag round trip: %v notModified=%v", err, again.NotModified)
	}
}
