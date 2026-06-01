package ubag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDiscoverSidecarFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	got := DiscoverSidecarAt(context.Background(), srv.URL, 200*time.Millisecond)
	if got != srv.URL {
		t.Fatalf("expected %s, got %q", srv.URL, got)
	}
}

func TestDiscoverSidecarAbsent(t *testing.T) {
	got := DiscoverSidecarAt(context.Background(), "http://127.0.0.1:1", 50*time.Millisecond)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
