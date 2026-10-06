package serve

import (
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
)

func TestExecutorLeaseTTLFromEnv(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"120000", 120 * time.Second, false},
		{"30000", 30 * time.Second, false},
		{"900000", 15 * time.Minute, false},
		{"29999", 0, true},
		{"900001", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range cases {
		t.Setenv("UBAG_EXECUTOR_LEASE_TTL_MS", tc.raw)
		got, err := executorLeaseTTLFromEnv()
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("raw %q: got %v, err %v; want %v, err=%v", tc.raw, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestFileDispatcherLeaseTTLWiring(t *testing.T) {
	t.Setenv("UBAG_EXECUTOR_MODE", "file")
	t.Setenv("UBAG_EXECUTOR_SPOOL_DIR", t.TempDir())

	t.Setenv("UBAG_EXECUTOR_LEASE_TTL_MS", "")
	d, err := newDispatcherFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if ttl := d.(*executor.FileSpoolDispatcher).LeaseTTL(); ttl != 0 {
		t.Fatalf("default lease ttl = %v, want 0 (legacy no-expiry)", ttl)
	}

	t.Setenv("UBAG_EXECUTOR_LEASE_TTL_MS", "120000")
	d, err = newDispatcherFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if ttl := d.(*executor.FileSpoolDispatcher).LeaseTTL(); ttl != 120*time.Second {
		t.Fatalf("lease ttl = %v, want 120s", ttl)
	}

	t.Setenv("UBAG_EXECUTOR_LEASE_TTL_MS", "5")
	if _, err := newDispatcherFromEnv(); err == nil {
		t.Fatal("out-of-range ttl must refuse to start")
	}
}
