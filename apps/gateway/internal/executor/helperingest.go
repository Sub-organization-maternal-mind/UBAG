package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// Helper ingest (UBAG_HELPER_PLANE, perf-fleet slice P4.9) is the primary-side
// gate between a helper node's attempt event stream (ubag.helper.v1
// AttemptEvent) and the job store. A helper is only partly trusted: it holds a
// lease on ONE attempt of ONE job, so everything it reports is checked against
// the trusted dispatch binding and the attempt ledger, bounded, projected into
// the gateway's own worker-event vocabulary, and committed through the fenced
// all-or-nothing CommitEvents (jobstore.FencedApplier). Nothing here trusts a
// message field for identity: the node id is the authenticated certificate
// identity, the tenant is the tenant the primary dispatched the attempt for.
//
// Two classes of failure, handled differently:
//
//   - Entitlement failures (wrong node, wrong tenant or job, wrong attempt, a
//     stale or superseded lease generation) and sequence gaps are REJECTED:
//     nothing is written, the rejection is audited, and the legitimate holder
//     of the attempt (if there is one) is unaffected. A fenced writer is
//     audited as attempt.fenced_rejected under UBAG-WORKER-NODE-FENCED-005.
//   - Content failures (event type outside the allowlist, malformed or
//     oversize data, a payload the store policy refuses, output over the
//     per-attempt budget, a "completed" outcome that is partial) FAIL
//     THE ATTEMPT: a terminal failed_terminal event is committed through the
//     same fence, with submitted/reconcile_required set once the prompt crossed
//     the submission boundary (D4). Over-limit output is never truncated into a
//     success.
//
// Inert: no gateway code constructs a HelperIngest yet; the dispatcher (later
// P4.x slices) is the first caller, behind UBAG_HELPER_PLANE.

const (
	// HelperFencedErrorCode is the catalog code (packages/shared-schemas/errors.json)
	// under which a fenced helper commit is audited.
	HelperFencedErrorCode = "UBAG-WORKER-NODE-FENCED-005"

	// DefaultHelperMaxEventBytes bounds one event's data_json and one outcome's
	// result_json. It sits below the store's own 64 KiB cap on the stored event
	// so the identity stamps added here always fit.
	DefaultHelperMaxEventBytes = 60 * 1024
	// DefaultHelperMaxAttemptBytes and DefaultHelperMaxAttemptEvents bound what
	// one attempt may push in total (every event becomes a stored row).
	DefaultHelperMaxAttemptBytes  = 4 << 20
	DefaultHelperMaxAttemptEvents = 4096

	// helperMaxMessageBytes bounds the helper-supplied error message.
	helperMaxMessageBytes = 512

	helperAuditFenced    = "attempt.fenced_rejected"
	helperAuditViolation = "helper.policy_violation"
)

var (
	// ErrHelperIngestScope: the writer is not entitled to this attempt (node,
	// tenant or job does not match the binding or the ledger). Nothing written.
	ErrHelperIngestScope = errors.New("helper ingest: not entitled to write this attempt")
	// ErrHelperIngestClosed: the attempt already ended (terminal committed,
	// failed by the gateway, or fenced); the session accepts nothing more.
	ErrHelperIngestClosed = errors.New("helper ingest: attempt already ended")
	// ErrHelperSequenceGap: the event skips ahead of the last committed sequence
	// (or restarts at zero). Rejected, not failed: a gap may only mean this is a
	// reconnect, so the caller re-opens with ResumeAfter and the helper replays.
	ErrHelperSequenceGap = errors.New("helper ingest: attempt event sequence gap")
	// ErrHelperEventInvalid: the event broke the helper event contract. Returned
	// after the attempt was failed (see Accept).
	ErrHelperEventInvalid = errors.New("helper ingest: invalid attempt event")
	// ErrHelperOutputLimit: the attempt exceeded its output budget. Returned
	// after the attempt was failed (see Accept); never truncated into a success.
	ErrHelperOutputLimit = errors.New("helper ingest: attempt output over limit")

	helperTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

	// helperEventTypes is the event-type allowlist: the non-terminal half of
	// ubag.helper.v1 AttemptEventType mapped to the gateway worker-event
	// vocabulary. TERMINAL is handled by terminal(); UNSPECIFIED and unknown
	// values are not in the map and fail closed. A helper can never emit a
	// job-lifecycle type of its own choosing (queued, assigned, dead_letter...).
	helperEventTypes = map[helperv1.AttemptEventType]string{
		helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_STARTED:                "running",
		helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_BROWSER_OPENED:         "browser_opened",
		helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_MANUAL_ACTION_REQUIRED: "session.manual_action_required",
		helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_PROMPT_SUBMITTED:       promptSubmittedEventType,
		helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TOKEN:                  "token",
		helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_ARTIFACT_CREATED:       "artifact_created",
		helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_WARNING:                "warning",
	}

	// helperStrippedKeys never cross from a helper into a job event: a helper
	// has no viewer broker (D3), and a loopback noVNC address on the helper's
	// host would point a primary-side reader at the wrong machine.
	helperStrippedKeys = []string{"novnc_url", "session_id"}
	// helperIdentityKeys, when a helper puts them in data, must equal the bound
	// value; a different one is a cross-tenant / cross-job / cross-node claim.
	helperIdentityKeys = []string{"job_id", "tenant_id", "app_id", "node_id", "attempt_id"}
	// helperOutcomeKeys are owned by the gateway on a terminal event: they are
	// derived from the outcome and never taken from data_json.
	helperOutcomeKeys = []string{"status", "result", "retryable", "submitted", "reconcile_required", "stream_end_reason", "error_code", "message", "error_class"}
)

// helperIngestError carries a sentinel kind plus a reason built only from
// gateway-computed text (never helper-supplied text).
type helperIngestError struct {
	kind   error
	reason string
}

func (e *helperIngestError) Error() string { return e.kind.Error() + ": " + e.reason }
func (e *helperIngestError) Unwrap() error { return e.kind }

func helperErr(kind error, format string, args ...any) error {
	return &helperIngestError{kind: kind, reason: fmt.Sprintf(format, args...)}
}

func helperReason(err error) string {
	var he *helperIngestError
	if errors.As(err, &he) {
		return he.reason
	}
	return "rejected"
}

// HelperIngestStore is the store surface helper ingest needs: the job read for
// the tenant binding plus the attempt ledger. jobstore.MemoryStore and
// jobstore.PostgresStore satisfy it; SQLite does not (it has no ledger).
type HelperIngestStore interface {
	Get(ctx context.Context, id string) (jobstore.Job, bool, error)
	ListAttempts(ctx context.Context, jobID string) ([]jobstore.Attempt, error)
	MarkSubmitted(ctx context.Context, ref jobstore.AttemptRef) (jobstore.Attempt, error)
	jobstore.FencedApplier
}

var (
	_ HelperIngestStore = (*jobstore.MemoryStore)(nil)
	_ HelperIngestStore = (*jobstore.PostgresStore)(nil)
)

// HelperIngestLimits are the per-attempt output bounds; zero values mean the
// defaults and MaxEventBytes is clamped to DefaultHelperMaxEventBytes.
type HelperIngestLimits struct {
	MaxEventBytes    int
	MaxAttemptBytes  int
	MaxAttemptEvents int
}

func (l HelperIngestLimits) normalized() HelperIngestLimits {
	if l.MaxEventBytes <= 0 || l.MaxEventBytes > DefaultHelperMaxEventBytes {
		l.MaxEventBytes = DefaultHelperMaxEventBytes
	}
	if l.MaxAttemptBytes <= 0 {
		l.MaxAttemptBytes = DefaultHelperMaxAttemptBytes
	}
	if l.MaxAttemptEvents <= 0 {
		l.MaxAttemptEvents = DefaultHelperMaxAttemptEvents
	}
	return l
}

// HelperIngestConfig wires an ingest session. Audit may be nil (rejections are
// then only logged).
type HelperIngestConfig struct {
	Store  HelperIngestStore
	Audit  audit.Store
	Limits HelperIngestLimits
}

// HelperIngestBinding is the trusted side of one attempt: where it must land
// and who may write it. None of it comes from a helper message.
type HelperIngestBinding struct {
	// TenantID is the tenant the primary dispatched the attempt for (from the
	// dispatch record, never from the helper). AppID is optional.
	TenantID string
	AppID    string
	JobID    string
	// AttemptID and Generation are the lease the ledger granted (BeginAttempt).
	AttemptID  string
	Generation uint64
	// NodeID is the authenticated certificate identity of the writing node
	// (helperauth.IdentityFromContext, or the pinned identity of the dial).
	NodeID string
	// InputFingerprint and WorkloadVersion are optional cross-checks against the
	// ledger row (proto: FAILED_PRECONDITION on mismatch).
	InputFingerprint string
	WorkloadVersion  string
	// ResumeAfter is the highest sequence the caller already knows is committed
	// for this attempt (0 for a fresh stream, which must start at 1). Events at
	// or below it are replays: idempotent at the store, free of budget.
	ResumeAfter uint64
}

// HelperBindingFromFence builds a binding from a helper-presented Fence. The
// fence's node_id is only a cross-check against the authenticated identity;
// tenantID must come from the primary's dispatch record for fence.job_id.
func HelperBindingFromFence(fence *helperv1.Fence, tenantID, authenticatedNodeID string) (HelperIngestBinding, error) {
	if fence == nil || tenantID == "" || authenticatedNodeID == "" || fence.GetNodeId() != authenticatedNodeID {
		return HelperIngestBinding{}, fmt.Errorf("%w: fence does not match the authenticated node", ErrHelperIngestScope)
	}
	return HelperIngestBinding{
		TenantID:         tenantID,
		JobID:            fence.GetJobId(),
		AttemptID:        fence.GetAttemptId(),
		Generation:       fence.GetLeaseGeneration(),
		NodeID:           authenticatedNodeID,
		InputFingerprint: fence.GetInputFingerprint(),
		WorkloadVersion:  fence.GetWorkloadVersion(),
	}, nil
}

// HelperIngest is one attempt's ingest session. It is safe for concurrent use
// but a stream is normally read by one goroutine.
type HelperIngest struct {
	store  HelperIngestStore
	audit  audit.Store
	b      HelperIngestBinding
	ref    jobstore.AttemptRef
	limits HelperIngestLimits

	// Immutable job facts captured at open.
	appID      string
	apiVersion string
	traceID    string

	mu          sync.Mutex
	lastSeq     uint64
	events      int
	bytes       int
	tokenEvents int
	submitted   bool
	done        bool
	audited     map[string]struct{}
}

// OpenHelperIngest validates the binding against the job and the attempt
// ledger once, up front. Tenant, app, node and attempt id are immutable on
// their rows, so this check cannot go stale; the live fence (generation and
// active state) is enforced again inside every commit's transaction.
func OpenHelperIngest(ctx context.Context, cfg HelperIngestConfig, b HelperIngestBinding) (*HelperIngest, error) {
	if cfg.Store == nil {
		return nil, errors.New("helper ingest: a store is required")
	}
	if b.TenantID == "" || b.JobID == "" || !jobstore.ValidAttemptID(b.AttemptID) || b.Generation == 0 || b.NodeID == "" {
		return nil, fmt.Errorf("%w: incomplete binding", ErrHelperIngestScope)
	}
	g := &HelperIngest{
		store: cfg.Store, audit: cfg.Audit, b: b, limits: cfg.Limits.normalized(),
		ref:     jobstore.AttemptRef{JobID: b.JobID, AttemptID: b.AttemptID, Generation: b.Generation},
		lastSeq: b.ResumeAfter,
		audited: map[string]struct{}{},
	}

	job, found, err := cfg.Store.Get(ctx, b.JobID)
	if err != nil {
		return nil, fmt.Errorf("helper ingest: load job: %w", err)
	}
	// A missing job and another tenant's job are indistinguishable to the caller.
	if !found || job.TenantID != b.TenantID || (b.AppID != "" && job.AppID != b.AppID) {
		g.deny(ctx, helperAuditViolation, "job_scope")
		return nil, ErrHelperIngestScope
	}
	g.appID, g.apiVersion, g.traceID = job.AppID, job.APIVersion, job.TraceID

	attempts, err := cfg.Store.ListAttempts(ctx, b.JobID)
	if err != nil {
		return nil, fmt.Errorf("helper ingest: load attempts: %w", err)
	}
	var attempt jobstore.Attempt
	var known bool
	for _, a := range attempts {
		if a.AttemptID == b.AttemptID {
			attempt, known = a, true
		}
	}
	switch {
	case !known:
		g.deny(ctx, helperAuditViolation, "unknown_attempt")
		return nil, ErrHelperIngestScope
	case attempt.NodeID != b.NodeID:
		g.deny(ctx, helperAuditViolation, "node_mismatch")
		return nil, ErrHelperIngestScope
	case (b.InputFingerprint != "" && b.InputFingerprint != attempt.InputFingerprint) ||
		(b.WorkloadVersion != "" && b.WorkloadVersion != attempt.WorkloadVersion):
		g.deny(ctx, helperAuditViolation, "fingerprint_mismatch")
		return nil, ErrHelperIngestScope
	case attempt.Generation != b.Generation:
		return nil, g.fenced(ctx, jobstore.ErrAttemptStale, "stale_generation")
	case attempt.State == jobstore.AttemptExpired:
		return nil, g.fenced(ctx, jobstore.ErrAttemptInactive, "attempt_expired")
	}
	g.submitted = attempt.Submitted()
	return g, nil
}

// Done reports whether the attempt ended in this session (the helper's terminal
// event committed, the gateway failed the attempt, or the writer was fenced).
// A done session accepts nothing more.
func (g *HelperIngest) Done() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.done
}

// Accept validates one helper event and commits it through the fence.
//
//   - nil error: committed (or an idempotent replay of an already committed
//     event); the returned job is its state after the commit.
//   - errors.Is(err, jobstore.ErrAttemptFenced): the writer was rejected as
//     stale or superseded; nothing was written and the failure is audited.
//   - errors.Is(err, ErrHelperIngestScope): the event claims another identity;
//     nothing was written and the violation is audited.
//   - errors.Is(err, ErrHelperSequenceGap): nothing was written, the session
//     stays open; re-open with ResumeAfter and have the helper replay.
//   - errors.Is(err, ErrHelperEventInvalid | ErrHelperOutputLimit): the helper
//     broke the contract and the ATTEMPT WAS FAILED (the returned job is its
//     failed state, Done() is true). Close the stream.
//   - any other error: a transient store failure; nothing was written and the
//     same event can be retried (commits are idempotent on the event key).
func (g *HelperIngest) Accept(ctx context.Context, ev *helperv1.AttemptEvent) (jobstore.Job, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.done {
		return jobstore.Job{}, ErrHelperIngestClosed
	}
	if ev == nil { // a programming error, not a helper message (gRPC never yields nil)
		return jobstore.Job{}, errors.New("helper ingest: nil event")
	}
	// Whose lease is this? A mismatch is a stale or confused writer.
	if ev.GetAttemptId() != g.b.AttemptID {
		return jobstore.Job{}, g.fenced(ctx, fmt.Errorf("%w: attempt id mismatch", jobstore.ErrAttemptFenced), "attempt_mismatch")
	}
	if ev.GetLeaseGeneration() != g.b.Generation {
		return jobstore.Job{}, g.fenced(ctx, jobstore.ErrAttemptStale, "stale_generation")
	}

	we, cost, replay, err := g.project(ev)
	if err != nil {
		if errors.Is(err, ErrHelperIngestScope) || errors.Is(err, ErrHelperSequenceGap) {
			g.deny(ctx, helperAuditViolation, helperReason(err))
			return jobstore.Job{}, err
		}
		return g.failAttempt(ctx, err)
	}

	// The submission boundary is recorded BEFORE the event lands: if the gateway
	// dies between the two, the ledger still knows the prompt may have left, so
	// nothing blindly resubmits it.
	if we.Type == promptSubmittedEventType && !g.submitted {
		if _, err := g.store.MarkSubmitted(ctx, g.ref); err != nil {
			return jobstore.Job{}, g.commitErr(ctx, err, "mark submitted")
		}
		g.submitted = true
	}
	job, err := g.store.CommitEvents(ctx, g.ref, []jobstore.WorkerEvent{we})
	if err != nil {
		return jobstore.Job{}, g.commitErr(ctx, err, "commit")
	}
	if !replay {
		g.lastSeq = ev.GetSequence()
		g.events++
		g.bytes += cost
		if we.Type == "token" {
			g.tokenEvents++
		}
	}
	// Only the helper's own terminal event ends the session. A job that went
	// terminal some other way (cancelled) keeps accepting events as no-ops, so a
	// reconnecting helper can replay everything and still get an ack; the caller
	// sees job.Status and cancels the helper.
	if ev.GetType() == helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL {
		g.done = true
	}
	return job, nil
}

// commitErr classifies a store error: a fenced writer is audited and the
// session closed for good (a superseded generation never comes back); anything
// else is transient and left for the caller to retry.
func (g *HelperIngest) commitErr(ctx context.Context, err error, op string) error {
	if errors.Is(err, jobstore.ErrAttemptFenced) {
		g.done = true
		return g.fenced(ctx, err, "commit_fenced")
	}
	return fmt.Errorf("helper ingest: %s: %w", op, err)
}

// project turns one helper event into the gateway WorkerEvent, enforcing the
// allowlist, the caps and the identity rules. cost is the bytes charged to the
// attempt budget; replay marks a sequence already committed in this session.
func (g *HelperIngest) project(ev *helperv1.AttemptEvent) (we jobstore.WorkerEvent, cost int, replay bool, err error) {
	seq := ev.GetSequence()
	switch {
	case seq == 0:
		return we, 0, false, helperErr(ErrHelperSequenceGap, "sequence starts at 1")
	case seq > g.lastSeq+1:
		return we, 0, false, helperErr(ErrHelperSequenceGap, "sequence %d after %d", seq, g.lastSeq)
	}
	replay = seq <= g.lastSeq

	terminal := ev.GetType() == helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL
	eventType, allowed := helperEventTypes[ev.GetType()]
	if !terminal && !allowed {
		return we, 0, false, helperErr(ErrHelperEventInvalid, "event type %d is not allowed", int32(ev.GetType()))
	}
	if !terminal && ev.GetOutcome() != nil {
		return we, 0, false, helperErr(ErrHelperEventInvalid, "an outcome is only allowed on a terminal event")
	}

	data, err := g.parseObject(ev.GetDataJson(), "data_json")
	if err != nil {
		return we, 0, false, err
	}
	cost = len(ev.GetDataJson())
	for _, key := range helperIdentityKeys {
		if value, present := data[key]; present && value != g.boundValue(key) {
			return we, 0, false, helperErr(ErrHelperIngestScope, "data names another %s", key)
		}
		delete(data, key)
	}
	for _, key := range helperStrippedKeys {
		delete(data, key)
	}

	if terminal {
		var resultBytes int
		if eventType, resultBytes, err = g.terminal(ev.GetOutcome(), data); err != nil {
			return we, 0, false, err
		}
		cost += resultBytes
	}
	if !replay {
		if g.events+1 > g.limits.MaxAttemptEvents {
			return we, 0, false, helperErr(ErrHelperOutputLimit, "more than %d events in one attempt", g.limits.MaxAttemptEvents)
		}
		if g.bytes+cost > g.limits.MaxAttemptBytes {
			return we, 0, false, helperErr(ErrHelperOutputLimit, "more than %d bytes in one attempt", g.limits.MaxAttemptBytes)
		}
	}

	// Node-namespaced provenance: the gateway stamps who produced the event under
	// its own keys, after (and over) anything the helper sent.
	data["attempt_id"] = g.b.AttemptID
	data["helper"] = map[string]any{"node_id": g.b.NodeID, "lease_generation": g.b.Generation}
	if err := jobstore.ValidateWorkerEventData(eventType, data); err != nil {
		return we, 0, false, helperErr(ErrHelperEventInvalid, "payload refused by the store policy")
	}
	return jobstore.WorkerEvent{
		// Event key = attempt_id:sequence (helper proto): per-attempt unique, so a
		// retried attempt never dedupes against its predecessor.
		EventID:    fmt.Sprintf("%s:%d", g.b.AttemptID, seq),
		JobID:      g.b.JobID,
		APIVersion: g.apiVersion,
		Type:       eventType,
		Sequence:   int(seq),
		Data:       data,
		TraceID:    g.traceID,
		CreatedAt:  time.Now().UTC(),
	}, cost, replay, nil
}

func (g *HelperIngest) boundValue(key string) any {
	switch key {
	case "job_id":
		return g.b.JobID
	case "tenant_id":
		return g.b.TenantID
	case "app_id":
		return g.appID
	case "node_id":
		return g.b.NodeID
	default: // attempt_id
		return g.b.AttemptID
	}
}

// parseObject decodes a bounded JSON object ("" and null are empty objects).
func (g *HelperIngest) parseObject(raw, what string) (map[string]any, error) {
	if len(raw) > g.limits.MaxEventBytes {
		return nil, helperErr(ErrHelperOutputLimit, "%s is %d bytes, limit %d", what, len(raw), g.limits.MaxEventBytes)
	}
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, helperErr(ErrHelperEventInvalid, "%s is not a JSON object", what)
	}
	if out == nil { // JSON null
		out = map[string]any{}
	}
	return out, nil
}

// terminal projects the outcome into the terminal event type and fills data
// (which already holds the helper's own extra data, minus identity keys). The
// gateway owns the outcome-derived keys. Deadline cuts end timed_out and a
// partial outcome is never a success (D4); a failure after the prompt crossed
// the submission boundary is failed_terminal with reconcile_required and is
// never retryable.
func (g *HelperIngest) terminal(out *helperv1.AttemptOutcome, data map[string]any) (eventType string, resultBytes int, err error) {
	if out == nil {
		return "", 0, helperErr(ErrHelperEventInvalid, "terminal event without an outcome")
	}
	for _, key := range helperOutcomeKeys {
		delete(data, key)
	}
	endReason, code := out.GetStreamEndReason(), out.GetErrorCode()
	if (endReason != "" && !helperTokenPattern.MatchString(endReason)) || (code != "" && !helperTokenPattern.MatchString(code)) {
		return "", 0, helperErr(ErrHelperEventInvalid, "stream_end_reason and error_code must be short tokens")
	}
	submitted := out.GetSubmitted() || g.submitted
	status := out.GetStatus()
	if status != helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED && out.GetResultJson() != "" {
		return "", 0, helperErr(ErrHelperEventInvalid, "result_json is only allowed on a completed outcome")
	}

	switch status {
	case helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED:
		if out.GetPartial() {
			return "", 0, helperErr(ErrHelperEventInvalid, "a completed outcome cannot be partial")
		}
		result, err := g.parseObject(out.GetResultJson(), "result_json")
		if err != nil {
			return "", 0, err
		}
		if len(result) == 0 {
			return "", 0, helperErr(ErrHelperEventInvalid, "a completed outcome needs a result")
		}
		resultBytes = len(out.GetResultJson())
		eventType = "completed"
		data["result"] = result
	case helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED:
		eventType, data["retryable"], data["error_class"] = "failed", true, "worker_execution"
		if submitted || out.GetReconcileRequired() {
			eventType, data["retryable"] = "failed_terminal", false
			data["reconcile_required"] = true
		}
	case helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT:
		eventType = "timed_out"
	case helperv1.AttemptStatus_ATTEMPT_STATUS_CANCELLED:
		eventType = "cancelled"
	default:
		return "", 0, helperErr(ErrHelperEventInvalid, "outcome status %d is not allowed", int32(status))
	}

	data["status"] = map[string]string{
		"completed": "completed", "failed": "failed_retryable", "failed_terminal": "failed_terminal",
		"timed_out": "timed_out", "cancelled": "cancelled",
	}[eventType]
	data["submitted"] = submitted
	if out.GetReconcileRequired() {
		data["reconcile_required"] = true
	}
	if endReason != "" {
		data["stream_end_reason"] = schemaStreamEndReason(endReason, status)
		if data["stream_end_reason"] != endReason {
			data["stream_end_detail"] = endReason // the helper's own token, outside the schema enum
		}
	}
	if code != "" {
		data["error_code"] = code
	}
	if msg := truncateUTF8(out.GetErrorMessage(), helperMaxMessageBytes); msg != "" {
		data["message"] = msg
	}
	// data.partial: the helper's own object when it sent one, else the count of
	// provisional token events this session saw. Never partial text in result.
	// It is reduced to the job-event schema's closed {text, token_events} shape.
	if out.GetPartial() {
		helperPartial, _ := data["partial"].(map[string]any)
		data["partial"] = schemaPartial(helperPartial, g.tokenEvents)
	} else {
		delete(data, "partial")
	}
	return eventType, resultBytes, nil
}

// failAttempt commits a terminal failure for an attempt whose helper broke the
// contract, through the same fence as any other write, and ends the session.
// It returns the failed job together with cause so the caller sees both.
func (g *HelperIngest) failAttempt(ctx context.Context, cause error) (jobstore.Job, error) {
	class := "helper_event_invalid"
	if errors.Is(cause, ErrHelperOutputLimit) {
		class = "helper_output_limit"
	}
	g.deny(ctx, helperAuditViolation, class)

	// Gateway-composed text only: nothing from the helper is echoed.
	we := jobstore.WorkerEvent{
		EventID:    g.b.AttemptID + ":gateway_failure",
		JobID:      g.b.JobID,
		APIVersion: g.apiVersion,
		Type:       "failed_terminal",
		TraceID:    g.traceID,
		CreatedAt:  time.Now().UTC(),
		Data: map[string]any{
			"status":             string(jobstore.StatusFailedTerminal),
			"retryable":          false,
			"error_class":        class,
			"message":            "helper output rejected by the gateway: " + helperReason(cause),
			"reason":             "helper_ingest_rejected",
			"submitted":          g.submitted,
			"reconcile_required": g.submitted,
			"stream_end_reason":  "error",
			"attempt_id":         g.b.AttemptID,
			"helper":             map[string]any{"node_id": g.b.NodeID, "lease_generation": g.b.Generation},
		},
	}
	// Outlive a cancelled stream context: the failure must still land.
	cctx, cancel := detachedOpContext(ctx)
	defer cancel()
	job, err := g.store.CommitEvents(cctx, g.ref, []jobstore.WorkerEvent{we})
	if err != nil {
		if errors.Is(err, jobstore.ErrAttemptFenced) {
			g.done = true
			return jobstore.Job{}, g.fenced(ctx, err, "commit_fenced")
		}
		// Transient: the session stays open so the same event can be retried (the
		// failure event key is stable); the lease reaper is the backstop.
		return jobstore.Job{}, errors.Join(cause, fmt.Errorf("helper ingest: record failure: %w", err))
	}
	g.done = true
	return job, cause
}

// fenced audits a fenced writer under UBAG-WORKER-NODE-FENCED-005 and returns
// cause (which satisfies errors.Is(.., jobstore.ErrAttemptFenced)).
func (g *HelperIngest) fenced(ctx context.Context, cause error, reason string) error {
	g.deny(ctx, helperAuditFenced, reason)
	return fmt.Errorf("%w (%s)", cause, HelperFencedErrorCode)
}

// deny records a rejection: one log line and one audit record per distinct
// (action, reason) per session, so a misbehaving helper cannot flood the
// tamper-evident chain. Audit failure never masks the rejection.
func (g *HelperIngest) deny(ctx context.Context, action, reason string) {
	// Metrics count every rejection (bounded labels); audit below is deduped.
	if action == helperAuditFenced {
		helpermetrics.RecordFencedReject(reason)
	} else {
		helpermetrics.RecordPolicyViolation(reason)
	}
	key := action + "/" + reason
	if _, seen := g.audited[key]; seen {
		return
	}
	g.audited[key] = struct{}{}
	slog.Warn("helper ingest rejected",
		"action", action, "reason", reason, "job_id", g.b.JobID, "attempt_id", g.b.AttemptID, "node_id", g.b.NodeID)
	if g.audit == nil {
		return
	}
	attrs := map[string]any{
		"reason": reason, "node_id": g.b.NodeID, "job_id": g.b.JobID,
		"attempt_id": g.b.AttemptID, "lease_generation": g.b.Generation,
	}
	if action == helperAuditFenced {
		attrs["error_code"] = HelperFencedErrorCode
	}
	actx, cancel := detachedOpContext(ctx)
	defer cancel()
	if _, err := g.audit.Append(actx, audit.Record{
		TenantID:   g.b.TenantID,
		AppID:      g.b.AppID,
		Actor:      "node:" + g.b.NodeID,
		Action:     action,
		Resource:   "job:" + g.b.JobID + "/attempt:" + g.b.AttemptID,
		Outcome:    "denied",
		OccurredAt: time.Now(),
		Attributes: attrs,
	}); err != nil {
		slog.Error("helper ingest audit append failed", "action", action, "error", err)
	}
}

// truncateUTF8 cuts s to at most max bytes on a rune boundary.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// jobEventStreamEndReasons is the closed stream_end_reason enum of
// job-event.schema.json.
var jobEventStreamEndReasons = map[string]bool{
	"provider_done": true, "deadline": true, "stalled": true, "cancelled": true, "error": true,
}

// schemaStreamEndReason maps a helper's free-form end reason onto the schema
// enum: a known value passes, anything else follows the outcome status.
func schemaStreamEndReason(reason string, status helperv1.AttemptStatus) string {
	switch {
	case jobEventStreamEndReasons[reason]:
		return reason
	case status == helperv1.AttemptStatus_ATTEMPT_STATUS_CANCELLED:
		return "cancelled"
	case status == helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT:
		return "deadline"
	}
	return "error"
}

// schemaPartial reduces a helper's partial object to the schema's closed
// {text, token_events} shape; unknown keys (for example truncated) are dropped
// and token_events falls back to the count of token events this session saw.
func schemaPartial(in map[string]any, tokenEvents int) map[string]any {
	out := map[string]any{"token_events": tokenEvents}
	if text, ok := in["text"].(string); ok {
		out["text"] = truncateUTF8(text, 1<<20)
	}
	switch n := in["token_events"].(type) {
	case float64:
		if n >= 0 {
			out["token_events"] = int(n)
		}
	case int:
		if n >= 0 {
			out["token_events"] = n
		}
	}
	return out
}
