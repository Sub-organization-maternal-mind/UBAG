package serve

import (
	"testing"
	"time"
)

type fakeNotifyStore struct{ fallback time.Duration }

func (f *fakeNotifyStore) EnableEventNotify(d time.Duration) { f.fallback = d }

func TestConfigureEventNotify(t *testing.T) {
	cases := []struct {
		name, mode, fallbackMS string
		wantFallback           time.Duration // 0 = hub must stay off
		wantErr                bool
	}{
		{"unset keeps legacy poll", "", "", 0, false},
		{"off keeps legacy poll", "off", "", 0, false},
		{"local default fallback", "local", "", 2 * time.Second, false},
		{"local custom fallback", "LOCAL", "500", 500 * time.Millisecond, false},
		{"postgres is reserved", "postgres", "", 0, false},
		{"unknown stays off", "bogus", "", 0, false},
		{"bad fallback fails fast", "local", "abc", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UBAG_EVENT_NOTIFY", tc.mode)
			t.Setenv("UBAG_EVENT_FALLBACK_MS", tc.fallbackMS)
			store := &fakeNotifyStore{}
			err := configureEventNotify(store)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if store.fallback != tc.wantFallback {
				t.Fatalf("fallback = %v, want %v", store.fallback, tc.wantFallback)
			}
		})
	}
}
