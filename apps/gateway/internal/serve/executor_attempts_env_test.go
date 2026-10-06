package serve

import (
	"strings"
	"testing"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

func TestConfigureExecutorAttempts(t *testing.T) {
	sqlite := jobstore.NewSQLiteStore(nil)
	cases := []struct {
		name, flag, kind string
		store            jobstore.Store
		wantErr          bool
	}{
		{"off is inert for sqlite", "", "sqlite", sqlite, false},
		{"explicit off is inert for sqlite", "false", "sqlite", sqlite, false},
		{"on with sqlite fails closed", "true", "sqlite", sqlite, true},
		{"on with memory", "1", "memory", jobstore.NewMemoryStore(), false},
		{"on with postgres", "yes", "postgres", jobstore.NewPostgresStore(nil), false},
		{"on with a store lacking the ledger", "true", "custom", struct{ jobstore.Store }{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UBAG_EXECUTOR_ATTEMPTS", tc.flag)
			err := configureExecutorAttempts(tc.store, tc.kind)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "UBAG_EXECUTOR_ATTEMPTS") {
				t.Fatalf("error %q does not name the flag", err)
			}
		})
	}
}
