package nodes

import "time"

// Drain grace bounds. A non-positive grace means "use the default" so a missing
// value can never turn into "cancel immediately".
const (
	DefaultDrainGrace = 5 * time.Minute
	MaxDrainGrace     = time.Hour
)

// Usage is what a helper is running right now. Attempts counts every active
// browser workload, voice calls included; Voice is the voice subset.
type Usage struct {
	Attempts int
	Voice    int
}

// ShrinkCap applies a (possibly lowered) admission target to current usage.
// capacity never drops below usage (a shrink never evicts running work) and
// free is how many new workloads may start now (0 until usage falls under the
// target).
func ShrinkCap(target int, u Usage) (capacity, free int) {
	target = max(target, 0)
	return max(target, u.Attempts), max(target-u.Attempts, 0)
}

// DrainPlan is what the caller should do for one helper this tick. Pure advice:
// the caller sends the Drain RPC and owns the cancel call.
type DrainPlan struct {
	// CallDrain: send the idempotent Drain RPC (stops new attempts on the node).
	CallDrain    bool
	GraceSeconds int32
	// CancelNonVoice: the grace period has elapsed, so non-voice attempts may be
	// cancelled. Voice calls are never cancelled by this policy.
	CancelNonVoice bool
	// VoiceHeld is the number of voice calls left to finish on their own.
	VoiceHeld int
	// Done: nothing is running; the node can be released.
	Done bool
}

// PlanDrain decides drain handling. Only grant state draining or revoked drains;
// active (and anything unknown) returns the zero plan, because cancelling work
// on an unrecognised state is not fail-safe (admission is already blocked by
// Evaluate). Finish in-flight first: cancel is allowed only after grace has
// elapsed since drainStart, and never when drainStart is unknown.
func PlanDrain(state string, u Usage, drainStart, now time.Time, grace time.Duration) DrainPlan {
	if state != StateDraining && state != StateRevoked {
		return DrainPlan{}
	}
	if grace <= 0 {
		grace = DefaultDrainGrace
	}
	grace = min(grace, MaxDrainGrace)
	voice := min(max(u.Voice, 0), max(u.Attempts, 0))
	p := DrainPlan{CallDrain: true, GraceSeconds: int32(grace / time.Second), VoiceHeld: voice, Done: u.Attempts <= 0}
	p.CancelNonVoice = !p.Done && u.Attempts > voice && !drainStart.IsZero() && now.Sub(drainStart) >= grace
	return p
}
