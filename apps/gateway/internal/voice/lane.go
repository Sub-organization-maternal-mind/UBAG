package voice

import (
	"context"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// LaneProbe tells the job consumer whether a live voice session holds a physical
// browser. The session's browser environment is a topology instance; the
// instance's registered CDP endpoint names the browser (topology.BrowserLaneKey),
// which is the same name a job's lane has. The answer comes from the voice store
// itself, so it covers every replica, every restart and a terminating hold, with
// no second copy of the lease to keep in step.
//
// It fails closed: a holder whose lane cannot be worked out (its instance is not
// in the topology, or has no endpoint) is treated as holding the lane, and an
// error is returned as an error so the caller holds its work back.
type LaneProbe struct {
	Store    Store
	Topology topology.Store
	// Now is injectable for tests; nil is time.Now().UTC().
	Now func() time.Time
}

// VoiceHoldsLane reports whether any session holds the lane right now.
func (p *LaneProbe) VoiceHoldsLane(ctx context.Context, lane string) (bool, error) {
	if p == nil || p.Store == nil || lane == "" {
		return false, nil
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now()
	}
	holders, err := p.Store.ListLeaseHolders(ctx, now)
	if err != nil || len(holders) == 0 {
		return false, err
	}
	lanes := map[string]map[string]string{} // tenant -> instance -> lane
	for _, holder := range holders {
		byInstance, ok := lanes[holder.TenantID]
		if !ok {
			if byInstance, err = p.instanceLanes(ctx, holder.TenantID); err != nil {
				return false, err
			}
			lanes[holder.TenantID] = byInstance
		}
		if held, known := byInstance[holder.InstanceRef]; !known || held == lane {
			return true, nil
		}
	}
	return false, nil
}

// instanceLanes maps a tenant's instances to their lane; an instance with no
// endpoint maps to nothing (its lane is unknown).
func (p *LaneProbe) instanceLanes(ctx context.Context, tenantID string) (map[string]string, error) {
	out := map[string]string{}
	if p.Topology == nil {
		return out, nil
	}
	instances, err := p.Topology.ListInstances(ctx, topology.InstanceFilter{TenantID: tenantID, Limit: 100})
	if err != nil {
		return nil, err
	}
	for _, instance := range instances {
		if lane := topology.BrowserLaneKey(instance.RemoteEndpoint); lane != "" {
			out[instance.InstanceID] = lane
		}
	}
	return out, nil
}
