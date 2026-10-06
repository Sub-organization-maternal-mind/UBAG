package jobs

// One vocabulary table for the job status state machine (ADR-0004 discipline
// at the gateway seam). statusTable is the single declaration every predicate
// and the worker-event mapping derive from; the 14 statuses are written out
// exactly once here.

type statusMeta struct {
	rank     int
	terminal bool
}

// statusTable is ordered by lifecycle rank. Terminal statuses share rank 100;
// early statuses rank below every terminal so no early state can jump to
// terminal via a data.status shortcut without a matching event type.
var statusTable = map[Status]statusMeta{
	StatusCreated:               {rank: 0, terminal: false},
	StatusScheduled:             {rank: 5, terminal: false},
	StatusQueued:                {rank: 10, terminal: false},
	StatusAssigned:              {rank: 20, terminal: false},
	StatusRunning:               {rank: 30, terminal: false},
	StatusTokenStreaming:        {rank: 40, terminal: false},
	StatusCompleting:            {rank: 50, terminal: false},
	StatusCompleted:             {rank: 100, terminal: true},
	StatusCompletedWithWarnings: {rank: 100, terminal: true},
	StatusFailedRetryable:       {rank: 100, terminal: true},
	StatusFailedTerminal:        {rank: 100, terminal: true},
	StatusDeadLetter:            {rank: 100, terminal: true},
	StatusCanceled:              {rank: 100, terminal: true},
	StatusTimedOut:              {rank: 100, terminal: true},
}

// workerEventTypes is the gateway's known-type set: the contract's 20 job-event
// types plus the worker's telemetry types and the aliases it emits. The single
// declaration behind knownWorkerEventType.
var workerEventTypes = map[string]struct{}{
	"created":                        {},
	"queued":                         {},
	"assigned":                       {},
	"running":                        {},
	"browser_opened":                 {},
	"session.opening":                {},
	"session.authenticated":          {},
	"session.manual_action_required": {},
	"prompt_submitted":               {},
	"token":                          {},
	"token_streaming":                {},
	"completing":                     {},
	"completed":                      {},
	"completed_with_warnings":        {},
	"failed":                         {},
	"failed_retryable":               {},
	"failed_terminal":                {},
	"dead_letter":                    {},
	"cancelled":                      {},
	"canceled":                       {},
	"timed_out":                      {},
	"timeout":                        {},
	"artifact_created":               {},
	"blocked":                        {},
	"warning":                        {},
}

func knownWorkerEventType(eventType string) bool {
	_, ok := workerEventTypes[eventType]
	return ok
}

// failureEventTypesTable is the failure-event vocabulary, built once at
// package init from workerEventStatus instead of re-enumerating the statuses
// (and re-allocating the map on every IsFailureEventType call).
var failureEventTypesTable = buildFailureEventTypes()

// buildFailureEventTypes derives the failure-event set from workerEventStatus:
// an event type is a failure event when its canonical status is one of the
// failure terminals (failed-retryable, failed-terminal, dead-letter, timed
// out). Canceled is deliberately excluded.
func buildFailureEventTypes() map[string]struct{} {
	set := make(map[string]struct{})
	for eventType := range workerEventTypes {
		status, ok := workerEventStatus(eventType, false)
		if !ok {
			continue
		}
		switch status {
		case StatusFailedRetryable, StatusFailedTerminal, StatusDeadLetter, StatusTimedOut:
			set[eventType] = struct{}{}
		}
	}
	return set
}

// failureEventTypes are the worker event types that represent job failure for
// signal reconstruction and error classification — derived from the
// workerEventStatus mapping, never re-enumerated at call sites.
func failureEventTypes() map[string]struct{} {
	return failureEventTypesTable
}

// IsFailureEventType reports whether a worker event type represents job
// failure. Consumers (httpapi signal reconstruction, error classification)
// call this instead of re-listing the failure vocabulary.
func IsFailureEventType(eventType string) bool {
	_, ok := failureEventTypes()[eventType]
	return ok
}

// shouldAdvanceStatus reports whether a transition from current to next is
// legal: never backwards, never from a terminal status, and next must be a
// declared status. All three stores and the API mutation path (UpdateStatus)
// route through this one rule.
func shouldAdvanceStatus(current Status, next Status) bool {
	meta, ok := statusTable[next]
	if !ok || current == next {
		return false
	}
	curMeta, ok := statusTable[current]
	if !ok || curMeta.terminal {
		return false
	}
	return meta.rank >= curMeta.rank
}

// EventEndsJob reports whether a worker event of this type (with its data)
// moves the job to a terminal status, using the same mapping the store applies
// (blocked and failed/failed_retryable are terminal: a retry is a new job).
func EventEndsJob(eventType string, data map[string]any) bool {
	retryable := true
	if r, ok := data["retryable"].(bool); ok {
		retryable = r
	}
	status, ok := workerEventStatus(eventType, retryable)
	return ok && TerminalStatus(status)
}

// workerEventStatus returns the canonical status for a worker event type,
// with the aliases the worker emits (canceled, timeout, failed, blocked)
// mapped in this one place. retryable distinguishes failed_retryable from
// failed_terminal for the "failed" family.
func workerEventStatus(eventType string, retryable bool) (Status, bool) {
	switch eventType {
	case "created":
		return StatusCreated, true
	case "queued":
		return StatusQueued, true
	case "assigned":
		return StatusAssigned, true
	case "running":
		return StatusRunning, true
	case "token", "token_streaming":
		return StatusTokenStreaming, true
	case "completing":
		return StatusCompleting, true
	case "completed":
		return StatusCompleted, true
	case "completed_with_warnings":
		return StatusCompletedWithWarnings, true
	case "failed", "failed_retryable":
		if retryable {
			return StatusFailedRetryable, true
		}
		return StatusFailedTerminal, true
	case "failed_terminal":
		return StatusFailedTerminal, true
	case "blocked":
		return StatusFailedRetryable, true
	case "dead_letter":
		return StatusDeadLetter, true
	case "cancelled", "canceled":
		return StatusCanceled, true
	case "timed_out", "timeout":
		return StatusTimedOut, true
	default:
		return "", false
	}
}
