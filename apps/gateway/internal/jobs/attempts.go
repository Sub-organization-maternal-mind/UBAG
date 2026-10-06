package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file is the Attempt Ledger contract (ADR-0007, perf-fleet slice P4.2).
//
// An attempt is one leased execution of a job on one node. Each attempt carries
// a per-job, strictly increasing lease generation; every write made on behalf of
// an attempt (renew, submission mark, event commit) presents the generation it
// was leased under, and the store rejects the write when that generation is
// stale or the attempt is no longer active. That is the fence: a node whose
// lease lapsed and was superseded can keep running, but nothing it reports can
// reach the job.
//
// The ledger is an OPTIONAL Store capability (like MetricsStore). It is inert
// unless UBAG_EXECUTOR_ATTEMPTS is on, and the local worker path keeps using
// plain Store.ApplyWorkerEvent. Memory and Postgres implement it; SQLite does
// not, and startup fails closed when the flag is on with a store that cannot
// fence (see serve.configureExecutorAttempts).

const (
	// DefaultAttemptLeaseTTL is the Attempt Lease TTL (ADR-0007: 120 s, renewed
	// every 20 s by the holder). A zero TTL on Begin/Renew means this value.
	DefaultAttemptLeaseTTL = 120 * time.Second
	// MaxAttemptLeaseTTL bounds a requested TTL so a buggy caller cannot park a
	// job behind a lease that effectively never lapses.
	MaxAttemptLeaseTTL = 15 * time.Minute
	// MaxAttemptCommitEvents bounds one CommitEvents batch.
	MaxAttemptCommitEvents = 256
	// MaxAttemptIDLength bounds attempt ids (matches the shared-schemas pattern).
	MaxAttemptIDLength = 128
	// maxAttemptFieldLength bounds node id / fingerprint / workload version.
	maxAttemptFieldLength = 256
	// DefaultExpireBatch and maxExpireBatch bound one ExpireAttempts sweep.
	DefaultExpireBatch = 100
	maxExpireBatch     = 1000
)

// AttemptState is the lifecycle state of one attempt. Transitions only move
// forward: active -> finished | expired.
type AttemptState string

const (
	// AttemptActive: the lease is held; the holder may renew, mark the
	// submission boundary and commit events.
	AttemptActive AttemptState = "active"
	// AttemptFinished: a commit drove the job terminal and closed the attempt.
	AttemptFinished AttemptState = "finished"
	// AttemptExpired: the lease lapsed and was reaped (ExpireAttempts) or a
	// successor attempt superseded it. Nothing it reports is accepted any more.
	AttemptExpired AttemptState = "expired"
)

var (
	// ErrAttemptFenced is the umbrella for "your write was rejected by the
	// fence": errors.Is(err, ErrAttemptFenced) means stop writing for this
	// attempt; it is a normal outcome of a lost lease, never a store failure.
	ErrAttemptFenced = errors.New("attempt fenced")
	// ErrAttemptStale: the writer presented a lease generation that is not the
	// attempt's own (a newer generation superseded it).
	ErrAttemptStale = fmt.Errorf("%w: stale lease generation", ErrAttemptFenced)
	// ErrAttemptInactive: the attempt is expired (or, for renew/submit,
	// finished); it can no longer write.
	ErrAttemptInactive = fmt.Errorf("%w: attempt is not active", ErrAttemptFenced)

	// ErrAttemptNotFound: the job or the attempt does not exist.
	ErrAttemptNotFound = errors.New("attempt not found")
	// ErrAttemptConflict: BeginAttempt lost the generation CAS (another
	// attempt began first), the previous lease is still held, or an attempt id
	// was reused with different parameters. Retriable after re-reading.
	ErrAttemptConflict = errors.New("attempt lease conflict")
	// ErrAttemptFingerprint: a new attempt carried a different input
	// fingerprint than the previous one; a retried attempt cannot silently
	// change its input.
	ErrAttemptFingerprint = errors.New("attempt input fingerprint changed")
	// ErrAttemptSubmitted: the previous attempt already crossed the submission
	// boundary, so starting another would risk a duplicate submit. The job
	// must be reconciled instead (decision D4), unless the caller verified the
	// provider never received it and sets AllowAfterSubmitted.
	ErrAttemptSubmitted = errors.New("previous attempt already submitted; reconcile instead of retrying")
	// ErrAttemptJobTerminal: the job is already terminal; no attempt may start.
	ErrAttemptJobTerminal = errors.New("job is terminal")
	// ErrAttemptInvalid wraps request validation failures.
	ErrAttemptInvalid = errors.New("invalid attempt request")
	// ErrAttemptEventMismatch: a committed event belongs to another job or was
	// stamped (data.attempt_id) by another attempt.
	ErrAttemptEventMismatch = errors.New("event does not belong to the committing attempt")
)

// Attempt is one ledger row.
type Attempt struct {
	JobID      string
	AttemptID  string
	Generation uint64 // per job, starts at 1, +1 per attempt
	NodeID     string // empty for local attempts
	State      AttemptState
	// LeaseExpiresAt is liveness, not a fence: a lapsed lease is only enforced
	// once BeginAttempt supersedes it or ExpireAttempts reaps it, so a late but
	// unsuperseded holder can still land finished work.
	LeaseExpiresAt   time.Time
	InputFingerprint string
	WorkloadVersion  string
	// SubmittedAt is the submission boundary (the prompt left for the
	// provider). Set once; nil until MarkSubmitted.
	SubmittedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	EndedAt     *time.Time
}

// Submitted reports whether the attempt crossed the submission boundary.
func (a Attempt) Submitted() bool { return a.SubmittedAt != nil }

// Ref returns the fence a writer for this attempt must present.
func (a Attempt) Ref() AttemptRef {
	return AttemptRef{JobID: a.JobID, AttemptID: a.AttemptID, Generation: a.Generation}
}

// AttemptRef is the fence presented by a writer: which attempt, under which
// lease generation.
type AttemptRef struct {
	JobID      string
	AttemptID  string
	Generation uint64
}

// BeginAttemptRequest starts the next attempt of a job.
type BeginAttemptRequest struct {
	JobID     string
	AttemptID string // ^att_[A-Za-z0-9]+$, see ValidAttemptID
	NodeID    string
	// ExpectedGeneration is the generation the caller last observed for the
	// job (0 when it has no attempt yet). The new attempt takes
	// ExpectedGeneration+1 only if that is still the next generation: this is
	// the compare-and-set that lets exactly one of two racing dispatchers win.
	ExpectedGeneration uint64
	// TTL is the lease length; 0 means DefaultAttemptLeaseTTL.
	TTL              time.Duration
	InputFingerprint string
	WorkloadVersion  string
	// AllowAfterSubmitted permits a new attempt although the previous one
	// already crossed the submission boundary. Only reconcile logic that has
	// verified the provider never received the prompt may set it.
	AllowAfterSubmitted bool
}

// AttemptStore is the optional Store capability behind UBAG_EXECUTOR_ATTEMPTS.
//
// Replays are no-ops: BeginAttempt with the parameters of an already-active
// attempt returns it unchanged; MarkSubmitted keeps the first timestamp;
// CommitEvents re-applying events dedupes on the worker event key.
type AttemptStore interface {
	// FencedApplier is the commit half of the ledger: CommitEvents (see its doc).
	FencedApplier
	// BeginAttempt leases the next attempt. It fails with ErrAttemptConflict
	// when ExpectedGeneration is not the job's latest generation or when the
	// latest attempt still holds an unexpired lease; a lapsed active
	// predecessor is marked expired in the same transaction. It also fails
	// with ErrAttemptJobTerminal, ErrAttemptFingerprint and ErrAttemptSubmitted
	// (see their docs).
	BeginAttempt(ctx context.Context, request BeginAttemptRequest) (Attempt, error)
	// RenewAttempt extends the lease to now+ttl (never shortens it). It fails
	// fenced (ErrAttemptStale / ErrAttemptInactive) unless the attempt is the
	// caller's active one.
	RenewAttempt(ctx context.Context, ref AttemptRef, ttl time.Duration) (Attempt, error)
	// MarkSubmitted records the submission boundary once; idempotent.
	MarkSubmitted(ctx context.Context, ref AttemptRef) (Attempt, error)
	// ExpireAttempts marks active attempts whose lease lapsed as expired and
	// returns them (oldest lease first, at most limit; 0 means
	// DefaultExpireBatch). After this returns, a late writer for such an
	// attempt is fenced. The caller decides the job's fate (retry, or fail with
	// reconcile_required when Submitted()).
	ExpireAttempts(ctx context.Context, limit int) ([]Attempt, error)
	// ListAttempts returns the job's attempts in ascending generation order.
	ListAttempts(ctx context.Context, jobID string) ([]Attempt, error)
}

// ValidAttemptID reports whether id matches ^att_[A-Za-z0-9]+$ (<=128 chars),
// the attempt id convention shared with the helper proto and worker schemas.
func ValidAttemptID(id string) bool {
	rest, ok := strings.CutPrefix(id, "att_")
	if !ok || rest == "" || len(id) > MaxAttemptIDLength {
		return false
	}
	for _, c := range rest {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func attemptTTL(ttl time.Duration) (time.Duration, error) {
	if ttl == 0 {
		return DefaultAttemptLeaseTTL, nil
	}
	if ttl < 0 || ttl > MaxAttemptLeaseTTL {
		return 0, fmt.Errorf("%w: ttl must be in (0, %s]", ErrAttemptInvalid, MaxAttemptLeaseTTL)
	}
	return ttl, nil
}

func (r AttemptRef) validate() error {
	if r.JobID == "" || !ValidAttemptID(r.AttemptID) || r.Generation == 0 {
		return fmt.Errorf("%w: job_id, attempt_id and generation are required", ErrAttemptInvalid)
	}
	return nil
}

func (r BeginAttemptRequest) validate() (time.Duration, error) {
	if r.JobID == "" || !ValidAttemptID(r.AttemptID) {
		return 0, fmt.Errorf("%w: job_id and a valid attempt_id are required", ErrAttemptInvalid)
	}
	if len(r.NodeID) > maxAttemptFieldLength || len(r.InputFingerprint) > maxAttemptFieldLength || len(r.WorkloadVersion) > maxAttemptFieldLength {
		return 0, fmt.Errorf("%w: node_id, input_fingerprint and workload_version are limited to %d bytes", ErrAttemptInvalid, maxAttemptFieldLength)
	}
	return attemptTTL(r.TTL)
}

// validateAttemptCommit checks everything about a commit batch that does not
// need the store: size, per-event shape (same rules as ApplyWorkerEvent), job
// scope, and attempt stamping.
func validateAttemptCommit(ref AttemptRef, events []WorkerEvent) error {
	if err := ref.validate(); err != nil {
		return err
	}
	if len(events) == 0 || len(events) > MaxAttemptCommitEvents {
		return fmt.Errorf("%w: commit needs 1..%d events", ErrAttemptInvalid, MaxAttemptCommitEvents)
	}
	for _, event := range events {
		if event.JobID != ref.JobID {
			return fmt.Errorf("%w: event %q is for job %q, not %q", ErrAttemptEventMismatch, event.EventID, event.JobID, ref.JobID)
		}
		if stamped, _ := event.Data["attempt_id"].(string); stamped != "" && stamped != ref.AttemptID {
			return fmt.Errorf("%w: event %q is stamped by attempt %q, not %q", ErrAttemptEventMismatch, event.EventID, stamped, ref.AttemptID)
		}
		if err := checkWorkerEvent(event); err != nil {
			return err
		}
	}
	return nil
}

// fenceAttempt is the single fence rule shared by every store. a is the stored
// attempt for ref.(JobID, AttemptID); found=false when there is none.
// allowFinished lets CommitEvents accept replays against a finished attempt.
func fenceAttempt(ref AttemptRef, a Attempt, found bool, allowFinished bool) error {
	switch {
	case !found:
		return fmt.Errorf("%w: attempt %s of job %s", ErrAttemptNotFound, ref.AttemptID, ref.JobID)
	case a.Generation != ref.Generation:
		return ErrAttemptStale
	case a.State == AttemptActive, a.State == AttemptFinished && allowFinished:
		return nil
	default:
		return ErrAttemptInactive
	}
}

// beginPlan is the outcome of planBegin: what the store has to write.
type beginPlan struct {
	attempt Attempt // the attempt to return (existing on replay, new otherwise)
	replay  bool    // nothing to write
	expire  string  // attempt id of a lapsed predecessor to mark expired
}

// planBegin decides a BeginAttempt against the job's attempt history
// (ascending generation). It is pure so memory and Postgres share one rule set;
// each store only serializes the read-decide-write (lock / transaction).
func planBegin(req BeginAttemptRequest, ttl time.Duration, history []Attempt, now time.Time) (beginPlan, error) {
	for _, a := range history {
		if a.AttemptID != req.AttemptID {
			continue
		}
		if a.NodeID != req.NodeID || a.InputFingerprint != req.InputFingerprint ||
			a.WorkloadVersion != req.WorkloadVersion || a.Generation != req.ExpectedGeneration+1 {
			return beginPlan{}, fmt.Errorf("%w: attempt id %s already used with different parameters", ErrAttemptConflict, a.AttemptID)
		}
		if a.State != AttemptActive {
			return beginPlan{}, ErrAttemptInactive
		}
		return beginPlan{attempt: a, replay: true}, nil
	}

	var latest Attempt
	if n := len(history); n > 0 {
		latest = history[n-1]
	}
	if latest.Generation != req.ExpectedGeneration {
		return beginPlan{}, fmt.Errorf("%w: job %s is at generation %d, expected %d", ErrAttemptConflict, req.JobID, latest.Generation, req.ExpectedGeneration)
	}
	plan := beginPlan{}
	if latest.Generation > 0 {
		if latest.InputFingerprint != req.InputFingerprint {
			return beginPlan{}, ErrAttemptFingerprint
		}
		if latest.State == AttemptActive {
			if now.Before(latest.LeaseExpiresAt) {
				return beginPlan{}, fmt.Errorf("%w: attempt %s holds the lease until %s", ErrAttemptConflict, latest.AttemptID, latest.LeaseExpiresAt.UTC().Format(time.RFC3339))
			}
			plan.expire = latest.AttemptID
		}
		if latest.SubmittedAt != nil && !req.AllowAfterSubmitted {
			return beginPlan{}, ErrAttemptSubmitted
		}
	}
	plan.attempt = Attempt{
		JobID:            req.JobID,
		AttemptID:        req.AttemptID,
		Generation:       latest.Generation + 1,
		NodeID:           req.NodeID,
		State:            AttemptActive,
		LeaseExpiresAt:   now.Add(ttl),
		InputFingerprint: req.InputFingerprint,
		WorkloadVersion:  req.WorkloadVersion,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	return plan, nil
}

func expireBatch(limit int) int {
	if limit <= 0 {
		return DefaultExpireBatch
	}
	return min(limit, maxExpireBatch)
}
