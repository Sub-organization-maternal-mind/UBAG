package runner

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// attemptRun maps one worker job's JSONL stream onto helper attempt events. It is
// used by exactly one ReadJob call, from one goroutine.
type attemptRun struct {
	spec helper.AttemptSpec
	emit helper.EmitFunc

	tokens    int
	submitted bool
	done      bool // the terminal event is written; later lines (telemetry) are dropped
}

// workerEvent is the part of a worker JSONL line the runner reads.
type workerEvent struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// onLine handles one non-control worker line. An error ends the job (and the
// daemon is discarded): a malformed line, or an attempt that can take no more
// events.
func (r *attemptRun) onLine(line string) error {
	var ev workerEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type == "" {
		return errors.New("worker emitted a malformed event")
	}
	if r.done {
		return nil
	}
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	switch ev.Type {
	case "session.opening":
		return r.progress(helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_BROWSER_OPENED, nil)
	case "session.manual_action_required":
		return r.progress(helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_MANUAL_ACTION_REQUIRED, map[string]any{
			"reason": token(ev.Data["reason"]),
		})
	case "prompt_submitted":
		r.submitted = true
		return r.progress(helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_PROMPT_SUBMITTED, nil)
	case "token":
		r.tokens++
		return r.progress(helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TOKEN, map[string]any{
			"token_index": ev.Data["token_index"], "delta": ev.Data["delta"],
		})
	case "completed", "completed_with_warnings":
		return r.terminal(&helperv1.AttemptOutcome{
			Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, Submitted: true, ResultJson: marshal(ev.Data["result"], "{}"),
		}, "")
	case "timed_out", "timeout":
		return r.timedOut(ev.Data)
	case "cancelled", "canceled":
		return r.terminal(&helperv1.AttemptOutcome{
			Status: helperv1.AttemptStatus_ATTEMPT_STATUS_CANCELLED, StreamEndReason: "cancelled", Submitted: r.submitted,
		}, "")
	case "failed", "failed_retryable", "failed_terminal", "dead_letter", "blocked":
		return r.failed(ev.Type, ev.Data)
	}
	// Everything else (queued, session.new_chat/configured/authenticated,
	// file.attached, concurrency, conversation and topology telemetry) is the
	// worker's own bookkeeping: none of it is part of the helper contract, and
	// forwarding unknown types would let a worker change what the primary sees.
	return nil
}

func (r *attemptRun) progress(t helperv1.AttemptEventType, data map[string]any) error {
	return r.emit(helper.Event{Type: t, DataJSON: marshal(data, "")})
}

func (r *attemptRun) terminal(o *helperv1.AttemptOutcome, dataJSON string) error {
	r.done = true
	return r.emit(helper.Event{Type: helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL, DataJSON: dataJSON, Outcome: o})
}

// timedOut is the D4 deadline cut: timed_out, partial flagged, the partial text
// on the terminal event's data and NEVER in the result.
func (r *attemptRun) timedOut(data map[string]any) error {
	reason := token(data["stream_end_reason"])
	if reason == "" {
		reason = "deadline"
	}
	partial, hasPartial := data["partial"].(map[string]any)
	dataJSON := ""
	if hasPartial {
		text, _ := partial["text"].(string)
		truncated := len(text) > maxPartialBytes
		if truncated {
			text = strings.ToValidUTF8(text[:maxPartialBytes], "")
		}
		dataJSON = marshal(map[string]any{"partial": map[string]any{
			"text": text, "token_events": partial["token_events"], "truncated": truncated,
		}}, "")
	}
	return r.terminal(&helperv1.AttemptOutcome{
		Status: helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT, StreamEndReason: reason,
		Partial: hasPartial || r.tokens > 0, Submitted: r.submitted || data["submitted"] == true,
	}, dataJSON)
}

// failed ends the attempt failed. Only a stable code and a short reason token
// cross to the primary; the worker's message text stays on the node.
func (r *attemptRun) failed(eventType string, data map[string]any) error {
	code := "helper_worker_failed"
	if eventType == "blocked" {
		code = "helper_worker_blocked"
	}
	if c, _ := data["error_code"].(string); errorCodeRe.MatchString(c) {
		code = c
	}
	reason := token(data["reason"])
	if reason == "" {
		reason = eventType
	}
	return r.terminal(&helperv1.AttemptOutcome{
		Status: helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED, StreamEndReason: reason, ErrorCode: code,
		ErrorMessage: "the attempt could not be completed on this node", Submitted: r.submitted,
	}, "")
}

// token returns v when it is a short identifier-like string, else "": reasons
// and similar enums may cross the trust boundary, free text may not.
func token(v any) string {
	s, _ := v.(string)
	if !tokenRe.MatchString(s) || utf8.RuneCountInString(s) > maxReasonRunes {
		return ""
	}
	return s
}

// marshal encodes v as JSON, or returns empty for nil and fallback on error.
func marshal(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return fallback
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return string(b)
}
