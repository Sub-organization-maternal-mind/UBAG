package helper

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

const (
	// maxAttemptsCeiling bounds Config.MaxAttempts whatever is configured: every
	// attempt is a whole browser. The operator-facing ceilings (1 workload at
	// first, 3 on 2c/4G, 5 on 4c/8G) live in the primary's node logic; this is
	// only the structural backstop.
	maxAttemptsCeiling = 8
	// maxKnownSlots bounds the identities remembered for ReportCapacity.
	maxKnownSlots = 256
	// maxReportedSlots bounds the identity slots in one capacity report.
	maxReportedSlots = 64
)

var (
	errCapacity     = errors.New("helper at its attempt capacity")
	errIdentityBusy = errors.New("provider identity already has an active attempt")
)

// identityKey is one physical provider session: (provider, opaque identity_ref).
type identityKey struct{ provider, ref string }

type slotEntry struct {
	state     helperv1.IdentitySlotState
	attemptID string
	seen      time.Time
}

// gate is the admission control of a Helper Node: a bound on concurrent
// attempts (browsers_max) and ONE active operation per (provider, identity_ref)
// (decision D6). It does bookkeeping only; the browsers belong to the Runner.
type gate struct {
	mu    sync.Mutex
	max   int
	slots map[identityKey]*slotEntry
}

func newGate(max int) *gate {
	return &gate{max: max, slots: map[identityKey]*slotEntry{}}
}

func (g *gate) busyLocked() int {
	n := 0
	for _, s := range g.slots {
		if s.state == helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_BUSY {
			n++
		}
	}
	return n
}

// acquire takes the identity and one unit of capacity for attemptID.
func (g *gate) acquire(k identityKey, attemptID string, now time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.slots[k]; s != nil && s.state == helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_BUSY {
		return errIdentityBusy
	}
	if g.busyLocked() >= g.max {
		return errCapacity
	}
	if g.slots[k] == nil && len(g.slots) >= maxKnownSlots {
		g.evictOldestIdleLocked()
	}
	g.slots[k] = &slotEntry{state: helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_BUSY, attemptID: attemptID, seen: now}
	return nil
}

// release frees the identity attemptID holds. manualAction records that the
// attempt stopped because a human must act on the provider session (login,
// challenge), so the slot reports MANUAL_ACTION_REQUIRED instead of IDLE.
func (g *gate) release(k identityKey, attemptID string, manualAction bool, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.slots[k]
	if s == nil || s.attemptID != attemptID {
		return
	}
	s.attemptID, s.seen = "", now
	s.state = helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_IDLE
	if manualAction {
		s.state = helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_MANUAL_ACTION_REQUIRED
	}
}

func (g *gate) evictOldestIdleLocked() {
	var oldest identityKey
	var at time.Time
	found := false
	for k, s := range g.slots {
		if s.state == helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_BUSY {
			continue
		}
		if !found || s.seen.Before(at) {
			oldest, at, found = k, s.seen, true
		}
	}
	if found {
		delete(g.slots, oldest)
	}
}

// active is how many attempts hold capacity right now.
func (g *gate) active() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.busyLocked()
}

// snapshot lists identity slots for a capacity report: busy ones first, then
// the rest, deterministic and bounded.
func (g *gate) snapshot() []*helperv1.IdentitySlot {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*helperv1.IdentitySlot, 0, len(g.slots))
	for k, s := range g.slots {
		out = append(out, &helperv1.IdentitySlot{
			Provider: k.provider, IdentityRef: k.ref, State: s.state, ActiveAttemptId: s.attemptID,
		})
	}
	slices.SortFunc(out, func(a, b *helperv1.IdentitySlot) int {
		if (a.State == helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_BUSY) != (b.State == helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_BUSY) {
			if a.State == helperv1.IdentitySlotState_IDENTITY_SLOT_STATE_BUSY {
				return -1
			}
			return 1
		}
		if a.Provider != b.Provider {
			return strings.Compare(a.Provider, b.Provider)
		}
		return strings.Compare(a.IdentityRef, b.IdentityRef)
	})
	if len(out) > maxReportedSlots {
		out = out[:maxReportedSlots]
	}
	return out
}
