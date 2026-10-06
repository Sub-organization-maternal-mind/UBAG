package helper

import (
	"context"
	"time"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// Runner executes ONE attempt on this Helper Node. The service never starts a
// browser itself: P4.12 plugs the warm-daemon pool in behind this interface and
// the tests plug a fake one.
//
// Contract:
//   - Run emits the attempt's events in order and finishes with exactly one
//     TERMINAL event (Event.Outcome set). It returns when the attempt is over.
//   - ctx is cancelled when the attempt must stop: CancelAttempt, lease expiry,
//     the attempt deadline, a newer lease generation, Drain grace or shutdown.
//     Run must stop its browser work promptly and return; it may emit its own
//     TERMINAL event first. If it does not, the service writes the terminal.
//   - emit returns an error once the attempt can take no more events (the
//     terminal was written, an output bound was hit). Run must then return.
//   - A Run that returns without a terminal (error, panic or nil) ends the
//     attempt `failed`; the error text is never forwarded to the primary.
//   - The one-active-operation-per-identity rule (decision D6) and the node's
//     concurrency bound are enforced BEFORE Run is called; the runner does not
//     need its own identity lock for correctness.
type Runner interface {
	Run(ctx context.Context, spec AttemptSpec, emit EmitFunc) error
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, spec AttemptSpec, emit EmitFunc) error

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, spec AttemptSpec, emit EmitFunc) error {
	return f(ctx, spec, emit)
}

// EmitFunc appends one event to the attempt. The service assigns sequence,
// generation and timestamp.
type EmitFunc func(Event) error

// Event is what a runner reports. Outcome is set only on the TERMINAL event.
type Event struct {
	Type     helperv1.AttemptEventType
	DataJSON string // a JSON object, or empty
	Outcome  *helperv1.AttemptOutcome
}

// AttemptSpec is the validated request a runner receives. It carries no
// credentials: IdentityRef is an opaque reference the runner maps to its own
// browser profile (P4.16).
type AttemptSpec struct {
	JobID       string
	AttemptID   string
	Generation  uint64
	Provider    string
	Target      string
	CommandType string
	IdentityRef string
	InputJSON   string
	OptionsJSON string
	Assets      []*helperv1.Asset
	TraceID     string
	// Deadline is the wall-clock budget; the service also enforces it from
	// outside (plus a short grace) so a runner that ignores it still ends.
	Deadline time.Duration
}
