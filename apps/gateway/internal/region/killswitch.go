package region

import (
	"context"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
)

// KillSwitch manages the current region's operational state and exposes
// convenience methods for the HTTP API and readiness probe.
type KillSwitch struct {
	registry *Registry
	current  func() Region
	audit    audit.Store // may be nil; audit is best-effort
}

// NewKillSwitch creates a KillSwitch that reads state from registry, determines
// the current region via current(), and emits audit records to auditStore
// (which may be nil to skip auditing).
func NewKillSwitch(registry *Registry, current func() Region, auditStore audit.Store) *KillSwitch {
	return &KillSwitch{
		registry: registry,
		current:  current,
		audit:    auditStore,
	}
}

// CurrentState returns the operational state of the current region.
// Returns StateActive safely when the receiver, registry, or current() is nil/empty.
func (k *KillSwitch) CurrentState(ctx context.Context) State {
	if k == nil || k.registry == nil || k.current == nil {
		return StateActive
	}
	r := k.current()
	if r == Region("") {
		return StateActive
	}
	s, err := k.registry.RegionState(ctx, r)
	if err != nil {
		return StateActive
	}
	return s
}

// IsAcceptingJobs reports whether the current region accepts new jobs.
// Returns false for both StateDraining and StateDisabled.
func (k *KillSwitch) IsAcceptingJobs(ctx context.Context) bool {
	s := k.CurrentState(ctx)
	return s == StateActive
}

// IsReady reports whether the current region is ready to serve traffic.
// Returns false only for StateDisabled; draining is still "ready" because
// in-flight jobs are allowed to finish and the load balancer keeps sending them.
func (k *KillSwitch) IsReady(ctx context.Context) bool {
	s := k.CurrentState(ctx)
	return s != StateDisabled
}

// SetState changes a named region's state in the registry and emits an audit
// record. Returns an error if the transition is invalid (from Registry.SetState).
// Audit emission is best-effort: if k.audit is nil or Append fails the error is
// silently dropped.
func (k *KillSwitch) SetState(ctx context.Context, r Region, newState State, actor, tenantID, appID string) error {
	if err := k.registry.SetState(ctx, r, newState); err != nil {
		return err
	}

	if k.audit != nil {
		_, _ = k.audit.Append(ctx, audit.Record{
			TenantID:   tenantID,
			AppID:      appID,
			Actor:      actor,
			Action:     "region:set_state",
			Resource:   "region:" + string(r),
			Outcome:    "state:" + string(newState),
			OccurredAt: time.Now(),
		})
	}

	return nil
}
