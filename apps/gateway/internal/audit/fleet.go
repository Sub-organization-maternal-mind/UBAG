package audit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// FleetTenant is the reserved tenant whose chain holds node lifecycle events.
// Attempt events go to the job's own tenant chain instead.
const FleetTenant = "_fleet"

// Fleet audit event names (vocabulary from P2.5; actor type is "node:<id>").
const (
	EventNodeEnrolled        = "node.enrolled"
	EventNodeCertRotated     = "node.cert_rotated"
	EventNodeRevoked         = "node.revoked"
	EventNodeAuthRejected    = "node.auth_rejected"
	EventAttemptGranted      = "attempt.granted"
	EventAttemptFencedReject = "attempt.fenced_rejected"
	EventAttemptCommitted    = "attempt.committed"
	EventAssetTokenIssued    = "asset.token_issued"
	EventHelperPolicyViolate = "helper.policy_violation"
	EventViewerOpened        = "viewer.opened"
)

// ErrAuditUnavailable is returned when a mandatory audit append fails. The
// guarded action has NOT run; callers must refuse the request (fail closed).
var ErrAuditUnavailable = errors.New("audit: append failed, action refused")

// Fleet appends fleet audit records to a Store.
type Fleet struct {
	Store Store
	Now   func() time.Time // nil = time.Now
}

func (f *Fleet) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// NodeEvent builds a node lifecycle record for the reserved _fleet chain.
func (f *Fleet) NodeEvent(action, nodeID, actor, outcome string, attrs map[string]any) Record {
	return Record{TenantID: FleetTenant, Actor: actor, Action: action, Resource: "node/" + nodeID,
		Outcome: outcome, OccurredAt: f.now(), Attributes: attrs}
}

// AttemptEvent builds an attempt record for the job's own tenant chain.
func (f *Fleet) AttemptEvent(tenantID, action, nodeID, jobID, outcome string, attrs map[string]any) Record {
	return Record{TenantID: tenantID, Actor: "node:" + nodeID, Action: action, Resource: "job/" + jobID,
		Outcome: outcome, OccurredAt: f.now(), Attributes: attrs}
}

// Guard appends rec and only then runs act. If the append fails, act is not
// run and ErrAuditUnavailable is returned: grant, enroll and viewer-open use
// this so no privileged action ever happens unaudited. (The pre-action record
// states intent; a failing act still leaves its record, which is the safe side.)
func (f *Fleet) Guard(ctx context.Context, rec Record, act func() error) error {
	if f == nil || f.Store == nil {
		return ErrAuditUnavailable
	}
	if _, err := f.Store.Append(ctx, rec); err != nil {
		return fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
	}
	return act()
}

// RejectAggregator folds auth rejections into one node.auth_rejected record
// per (reason, node) per flush, so a flood of bad handshakes is one chain
// entry with a count rather than one entry per attempt.
type RejectAggregator struct {
	fleet *Fleet
	max   int

	mu     sync.Mutex
	counts map[[2]string]int
}

// NewRejectAggregator keeps at most maxKeys distinct (reason, node) pairs per
// window; further pairs are counted under node "other".
func NewRejectAggregator(f *Fleet, maxKeys int) *RejectAggregator {
	if maxKeys <= 0 {
		maxKeys = 256
	}
	return &RejectAggregator{fleet: f, max: maxKeys, counts: map[[2]string]int{}}
}

// Observe matches helperauth.Authenticator.OnReject (string-typed to keep this
// package free of helperauth). It never touches the store.
func (a *RejectAggregator) Observe(reason, nodeID string) {
	k := [2]string{reason, nodeID}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.counts[k]; !ok && len(a.counts) >= a.max {
		k = [2]string{reason, "other"}
	}
	a.counts[k]++
}

// Flush appends one record per pending key. Rejections are already refused, so
// a failed append cannot fail an action; the counts are re-queued for the next
// flush and the first error is returned for logging.
func (a *RejectAggregator) Flush(ctx context.Context) error {
	a.mu.Lock()
	pending := a.counts
	a.counts = map[[2]string]int{}
	a.mu.Unlock()

	keys := make([][2]string, 0, len(pending))
	for k := range pending {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	var firstErr error
	for _, k := range keys {
		rec := a.fleet.NodeEvent(EventNodeAuthRejected, k[1], "node:"+k[1], "deny",
			map[string]any{"reason": k[0], "count": pending[k]})
		if _, err := a.fleet.Store.Append(ctx, rec); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			a.mu.Lock()
			a.counts[k] += pending[k]
			a.mu.Unlock()
		}
	}
	return firstErr
}

// Run flushes every interval until ctx ends, then once more.
func (a *RejectAggregator) Run(ctx context.Context, interval time.Duration, onErr func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := a.Flush(fctx); err != nil && onErr != nil {
				onErr(err)
			}
			cancel()
			return
		case <-t.C:
			if err := a.Flush(ctx); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}
