package executor

import (
	"context"
	"errors"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// placementRetryDelay is how long a job held by the placer waits before its lease
// is retried (ADR-0015): short, because the hold happens off the worker, and the
// same as the pool-overload delay (ADR-0011).
const placementRetryDelay = defaultOverloadRetryDelay

// FleetPicker is the HelperPicker over the accepted manager grants (P4.17, ADR-0015):
// it joins the tenant's own profiles (helperauth) with the capacity the placer
// tracks (nodes). It runs AFTER the lease, because the target and the identity
// are only known from the envelope and the queue cannot peek by eligibility.
//
// The answer for a leased job, in order:
//
//   - no helper node is eligible at all (nothing granted, manager unreachable past
//     the stale grace, every node silent or draining): ErrNoHelper, run here
//     (ADR-0014; decision D3 grants nothing new while the manager is down);
//   - the tenant has no active profile for the target: ErrNoHelper, it is not on
//     the fleet;
//   - a conversation that cannot resume on a helper: ErrNoHelper, the worker
//     applies its on_missing policy;
//   - the tenant has profiles but none on an eligible node, the conversation's
//     bound node is down, every candidate's identity lane is busy, or every
//     eligible node is full: a *HelperRetryError, the job is held and its lease
//     retried after a delay. Never a failure and never another tenant's or an
//     unbound profile (F9);
//   - otherwise a placement: a node slot and the profile's identity lane reserved
//     together, released together exactly once.
//
// A store error reading the fleet or the profiles is a hold, never a local run:
// "could not tell" must not move affinity-bound work onto this gateway.
type FleetPicker struct {
	Placer   *nodes.Placer
	Profiles helperauth.ProfileStore
	// RetryAfter is the hold before the lease is retried; zero means 2 s.
	RetryAfter time.Duration
}

var _ HelperPicker = (*FleetPicker)(nil)

func (p *FleetPicker) retryAfter() time.Duration {
	if p.RetryAfter > 0 {
		return p.RetryAfter
	}
	return placementRetryDelay
}

// Pick implements HelperPicker.
func (p *FleetPicker) Pick(ctx context.Context, req HelperPickRequest) (HelperPlacement, error) {
	hold := func(outcome, reason string, cause error) (HelperPlacement, error) {
		helpermetrics.RecordPlacement(outcome)
		return HelperPlacement{}, &HelperRetryError{Reason: reason, RetryAfter: p.retryAfter(), Err: cause}
	}
	local := func(outcome string) (HelperPlacement, error) {
		helpermetrics.RecordPlacement(outcome)
		return HelperPlacement{}, ErrNoHelper
	}

	fleet, err := p.Placer.Nodes(ctx)
	if err != nil {
		return hold("held_error", "fleet_unreadable", err)
	}
	// A conversation bound to a helper waits for it even when no node is eligible:
	// it never relocates (F9). Everything else runs here when there is no fleet.
	if len(fleet) == 0 && (req.Conversation == nil || req.Conversation.NodeID == "") {
		return local("local_no_fleet")
	}
	eligible := make(map[string]bool, len(fleet))
	for _, n := range fleet {
		eligible[n.ID] = true
	}

	bindings, err := p.Profiles.List(ctx, req.TenantID, req.Target)
	if err != nil {
		return hold("held_error", "profiles_unreadable", err)
	}
	if len(bindings) == 0 && req.Conversation == nil {
		return local("local_no_profile")
	}
	candidates, err := helperauth.EligibleProfiles(helperauth.SelectInput{
		TenantID: req.TenantID, Provider: req.Target, Bindings: bindings, Conversation: req.Conversation,
		NodeEligible: func(nodeID string) bool { return eligible[nodeID] },
	})
	switch {
	case errors.Is(err, helperauth.ErrAffinityUnavailable):
		return hold("held_affinity", "affinity_unavailable", err)
	case errors.Is(err, helperauth.ErrConversationBroken):
		return local("local_conversation")
	case errors.Is(err, helperauth.ErrNoMatchingProfile):
		return hold("held_no_node", "no_eligible_node", err)
	case err != nil:
		return hold("held_error", "profile_selection", err)
	}

	busy := false
	for _, b := range candidates {
		res, err := p.Placer.Reserve(ctx, b.NodeID, b.ProfileRef)
		switch {
		case err == nil:
			helpermetrics.RecordPlacement("placed")
			return HelperPlacement{NodeID: res.Node.ID, Endpoint: res.Node.Endpoint, ProfileRef: b.ProfileRef, Release: res.Release}, nil
		case errors.Is(err, nodes.ErrLaneBusy):
			busy = true
		case errors.Is(err, nodes.ErrNoCapacity), errors.Is(err, nodes.ErrNodeNotEligible):
			// Another candidate may still fit. A node that fell out of the view
			// since it was read is, for this job, the same as a full one.
		default:
			return hold("held_error", "reserve_failed", err)
		}
	}
	if busy {
		return hold("held_identity_busy", "identity_busy", nodes.ErrLaneBusy)
	}
	return hold("held_no_capacity", "no_capacity", nodes.ErrNoCapacity)
}
