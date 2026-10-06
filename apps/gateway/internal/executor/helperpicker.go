package executor

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/attachments"
	"github.com/ubag/ubag/apps/gateway/internal/conversations"
	"github.com/ubag/ubag/apps/gateway/internal/jobcore"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// Helper placement (UBAG_HELPER_DISPATCH, perf-fleet slice P4.14).
//
// The consumer asks a HelperPicker where a leased job runs, AFTER the job is
// leased and BEFORE it is assigned (lease-then-place, ADR-0011): the target and
// identity are only known from the envelope. The picker is an interface on
// purpose. The real implementation is outside this file (the placer over the
// accepted manager grants, P4.17, or an adaptor for the fleet manager, which has
// no project-facing API); everything the runner needs from it is one answer:
//
//   - a placement: run the attempt on this Helper Node, under this profile;
//   - ErrNoHelper: nothing is placed, run the job on this gateway (the
//     ineligible-target and manager-unreachable answer: "grant nothing new");
//   - a *HelperRetryError: a helper is wanted but cannot take the job right now
//     (identity busy, bound node down); hold the job back and retry, never fail it.

// ErrNoHelper is the picker's "no placement": the job runs on the local runner.
var ErrNoHelper = errors.New("helper dispatch: no helper placement; run on this gateway")

// HelperRetryError says the job could not start on a helper yet and must be
// held back: it never reached a provider, so it is neither failed nor completed.
// The consumer answers it with a delayed lease Retry (retryAfterDelay), the same
// as a saturated worker pool: Retry re-queues instantly on the file spool, so
// without the delay lease -> refuse -> Retry would spin.
type HelperRetryError struct {
	// Reason is a short fixed token (it reaches logs and nothing else).
	Reason     string
	RetryAfter time.Duration
	// Err is the cause, if any (errors.Is sees through it).
	Err error
}

func (e *HelperRetryError) Error() string {
	if e.Err != nil {
		return "helper dispatch: job held back (" + e.Reason + "): " + e.Err.Error()
	}
	return "helper dispatch: job held back (" + e.Reason + ")"
}

func (e *HelperRetryError) Unwrap() error { return e.Err }

// HelperPickRequest is what the picker may decide on: who the job belongs to and
// what it targets. The payload (input, options) never reaches the picker.
type HelperPickRequest struct {
	TenantID    string
	AppID       string
	JobID       string
	Target      string
	CommandType string
	TraceID     string
	// Conversation is the job's stored conversation binding, when it has one. Place
	// never sets it today: helperEligible keeps every conversation job on this
	// gateway until the helper contract can carry a conversation block. The placer
	// honours it already (resume only on the bound node and profile).
	Conversation *conversations.Conversation
}

// HelperPicker chooses where a leased job runs. Implementations must be safe for
// concurrent use. Pick returns a placement, ErrNoHelper (run locally) or a
// *HelperRetryError (hold the job back); any other error is treated like a
// retry, never like ErrNoHelper, so a broken picker cannot silently move
// affinity-bound work onto this gateway.
type HelperPicker interface {
	Pick(ctx context.Context, req HelperPickRequest) (HelperPlacement, error)
}

// HelperPlacement is one granted slot on a Helper Node. Nothing in it is a
// credential.
type HelperPlacement struct {
	// NodeID is the node the attempt runs on. It is the identity the dial must
	// authenticate (URI SAN spiffe://ubag/node/<NodeID>, SPKI-pinned).
	NodeID string
	// Endpoint is the helper's mTLS gRPC address (host:port on WireGuard).
	Endpoint string
	// ProfileRef is the opaque, tenant-owned profile handle (P4.16) the helper
	// maps to its own browser profile; it is also the identity lane key.
	ProfileRef string
	// Release returns whatever the picker reserved (a workload slot, an identity
	// lane). It is called exactly once, when the attempt ends in any way or the
	// placement is dropped. Nil means nothing to release.
	Release func()
}

// NoHelperPicker places nothing: every job runs on this gateway. It is what
// UBAG_HELPER_DISPATCH used before the placer existed; serve now wires the
// FleetPicker (helperplacer.go), and this stays for tests.
type NoHelperPicker struct{}

func (NoHelperPicker) Pick(context.Context, HelperPickRequest) (HelperPlacement, error) {
	return HelperPlacement{}, ErrNoHelper
}

// Placement is a granted placement whose Release is idempotent.
type Placement struct {
	HelperPlacement
	once sync.Once
}

// Release frees the picker's reservation; safe on nil and to call twice.
func (p *Placement) Release() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		if p.HelperPlacement.Release != nil {
			p.HelperPlacement.Release()
		}
	})
}

// helperNameRe mirrors the helper's own identifier rule for provider, target and
// command_type (internal/helper nameRe): a job the helper would refuse as
// malformed must stay on this gateway instead of failing there.
var helperNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// maxHelperTraceIDBytes mirrors the helper's trace_id bound.
const maxHelperTraceIDBytes = 128

// helperEligible reports whether a job may run on a helper at all. It is the
// static half of eligibility (the picker owns capacity and affinity); a job that
// fails it never reaches the picker and runs on this gateway:
//
//   - antigravity_* holds gateway-side credentials and never leaves (ADR-0002);
//   - voice.* command types drive the gateway's own browser;
//   - conversation jobs: the helper contract (RunAttempt) has no conversation
//     block and no conversation event, so a bound thread cannot resume there;
//   - declared attachments: the primary-side asset manifest and attempt token
//     for the helper's staging pull are not part of this slice;
//   - names the helper would refuse, and any job whose projection the payload
//     policy refuses.
func helperEligible(env DispatchEnvelope) bool {
	job := env.Job
	switch {
	case !helperNameRe.MatchString(job.Target), !helperNameRe.MatchString(job.CommandType),
		strings.HasPrefix(strings.ToLower(job.Target), "antigravity_"),
		jobcore.IsReservedCommandType(job.CommandType),
		job.ConversationID != "", env.Conversation != nil,
		len(env.TraceID) > maxHelperTraceIDBytes:
		return false
	}
	if declared, err := attachments.DeclaredAttachments(job.Input); err != nil || len(declared) > 0 {
		return false
	}
	_, _, err := projectHelperJob(env, "att_eligibility", "pr_eligibility")
	return err == nil
}

// validPlacement checks the shape of a picker answer before anything is dialed.
// A profile ref with a ':' passes the spec pattern but not the worker's, and the
// helper would end the attempt for it; refuse it here instead.
func validPlacement(p HelperPlacement) bool {
	return nodes.ValidNodeID(p.NodeID) && p.Endpoint != "" && len(p.Endpoint) <= 270 &&
		helperProfileRefPattern.MatchString(p.ProfileRef) && !strings.Contains(p.ProfileRef, ":")
}
