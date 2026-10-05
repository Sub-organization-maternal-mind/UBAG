package serve

import (
	"net"
	"net/http"
	"testing"
)

func TestPprofUnsetStartsNoListener(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		stop, err := startPprof(raw)
		if err != nil {
			t.Fatalf("startPprof(%q) err = %v", raw, err)
		}
		stop()
	}
}

func TestPprofRefusesNonLoopback(t *testing.T) {
	for _, raw := range []string{":6060", "0.0.0.0:6060", "[::]:6060", "10.0.0.5:6060", "example.com:6060", "6060"} {
		if _, err := startPprof(raw); err == nil {
			t.Fatalf("startPprof(%q) = nil error, want refusal", raw)
		}
	}
}

func TestPprofServesOnLoopback(t *testing.T) {
	// Reserve a free loopback port, release it, then ask startPprof to use it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	stop, err := startPprof(addr)
	if err != nil {
		t.Fatalf("startPprof(%q): %v", addr, err)
	}
	defer stop()
	resp, err := http.Get("http://" + addr + "/debug/pprof/cmdline")
	if err != nil {
		t.Fatalf("GET pprof: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
