package region

import (
	"context"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
)

// TestKillSwitchIsAcceptingJobs verifies that IsAcceptingJobs returns false for
// both draining and disabled states, and true for active.
func TestKillSwitchIsAcceptingJobs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state    State
		expected bool
	}{
		{StateActive, true},
		{StateDraining, false},
		{StateDisabled, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()
			store := NewMemoryStateStore()
			reg := NewRegistry(store)
			r := Region("test-region")
			if err := store.Set(r, tc.state); err != nil {
				t.Fatalf("seed state: %v", err)
			}
			ks := NewKillSwitch(reg, func() Region { return r }, nil)
			got := ks.IsAcceptingJobs(context.Background())
			if got != tc.expected {
				t.Fatalf("IsAcceptingJobs for %v = %v, want %v", tc.state, got, tc.expected)
			}
		})
	}
}

// TestKillSwitchIsReady verifies that IsReady returns false only for disabled —
// active and draining are both "ready" (draining allows in-flight jobs to finish).
func TestKillSwitchIsReady(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state    State
		expected bool
	}{
		{StateActive, true},
		{StateDraining, true}, // draining is still ready; in-flight can finish
		{StateDisabled, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()
			store := NewMemoryStateStore()
			reg := NewRegistry(store)
			r := Region("test-region")
			if err := store.Set(r, tc.state); err != nil {
				t.Fatalf("seed state: %v", err)
			}
			ks := NewKillSwitch(reg, func() Region { return r }, nil)
			got := ks.IsReady(context.Background())
			if got != tc.expected {
				t.Fatalf("IsReady for %v = %v, want %v", tc.state, got, tc.expected)
			}
		})
	}
}

// TestKillSwitchSetStateAudits verifies that SetState emits an audit record
// when a non-nil audit store is provided.
func TestKillSwitchSetStateAudits(t *testing.T) {
	t.Parallel()
	store := NewMemoryStateStore()
	reg := NewRegistry(store)
	r := Region("us-east-1")
	auditStore := audit.NewMemoryStore()
	ks := NewKillSwitch(reg, func() Region { return r }, auditStore)

	if err := ks.SetState(context.Background(), r, StateDraining, "actor", "t", "a"); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	records, err := auditStore.List(context.Background(), audit.Filter{TenantID: "t"})
	if err != nil {
		t.Fatalf("List audit records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(records))
	}
	if records[0].Action != "region:set_state" {
		t.Fatalf("audit record Action = %q, want %q", records[0].Action, "region:set_state")
	}
}

// TestKillSwitchNilSafe verifies that calling methods on a nil *KillSwitch
// returns safe defaults (true for both IsAcceptingJobs and IsReady).
func TestKillSwitchNilSafe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var ks *KillSwitch

	if got := ks.IsAcceptingJobs(ctx); !got {
		t.Fatal("nil KillSwitch.IsAcceptingJobs() = false, want true")
	}
	if got := ks.IsReady(ctx); !got {
		t.Fatal("nil KillSwitch.IsReady() = false, want true")
	}
}

// TestKillSwitchSetStateEmitsAudit verifies full audit record contents after SetState.
func TestKillSwitchSetStateEmitsAudit(t *testing.T) {
	t.Parallel()
	store := NewMemoryStateStore()
	reg := NewRegistry(store)
	r := Region("eu-west-1")
	auditStore := audit.NewMemoryStore()
	ks := NewKillSwitch(reg, func() Region { return r }, auditStore)

	const (
		actor    = "admin@example.com"
		tenantID = "tenant-abc"
		appID    = "app-xyz"
	)

	if err := ks.SetState(context.Background(), r, StateDraining, actor, tenantID, appID); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	records, err := auditStore.List(context.Background(), audit.Filter{TenantID: tenantID})
	if err != nil {
		t.Fatalf("List audit records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(records))
	}
	rec := records[0]
	if rec.Action != "region:set_state" {
		t.Errorf("Action = %q, want %q", rec.Action, "region:set_state")
	}
	if rec.Resource != "region:"+string(r) {
		t.Errorf("Resource = %q, want %q", rec.Resource, "region:"+string(r))
	}
	if rec.Outcome != "state:"+string(StateDraining) {
		t.Errorf("Outcome = %q, want %q", rec.Outcome, "state:"+string(StateDraining))
	}
	if rec.Actor != actor {
		t.Errorf("Actor = %q, want %q", rec.Actor, actor)
	}
	if rec.TenantID != tenantID {
		t.Errorf("TenantID = %q, want %q", rec.TenantID, tenantID)
	}
	if rec.AppID != appID {
		t.Errorf("AppID = %q, want %q", rec.AppID, appID)
	}
}
