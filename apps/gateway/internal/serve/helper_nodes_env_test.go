package serve

import (
	"context"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

func TestHelperNodeStoreFromEnv(t *testing.T) {
	cases := []struct {
		name, flag, storeKind string
		wantStore             bool
		wantErr               string // substring; "" = no error
	}{
		{"unset is inert", "", "memory", false, ""},
		{"off is inert", "false", "postgres", false, ""},
		{"inert even on sqlite", "", "sqlite", false, ""},
		{"memory", "true", "memory", true, ""},
		{"memory via 1", "1", "memory", true, ""},
		{"sqlite refuses", "true", "sqlite", false, "not supported"},
		{"unknown store refuses", "true", "bogus", false, "not supported"},
		{"postgres needs a handle", "true", "postgres", false, "requires a Postgres handle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UBAG_HELPER_NODES", tc.flag)
			store, err := newHelperNodeStoreFromEnv(context.Background(), tc.storeKind, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (store != nil) != tc.wantStore {
				t.Fatalf("store = %v, wantStore %v", store, tc.wantStore)
			}
			if tc.wantStore {
				if _, ok := store.(*nodes.MemoryStore); !ok {
					t.Fatalf("store = %T, want *nodes.MemoryStore", store)
				}
			}
		})
	}
}
