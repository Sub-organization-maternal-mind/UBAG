package httpapi

import (
	"context"

	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// jobReservation owns the cleanup of an in-flight job creation: the
// idempotency scope, the concurrency token, and the job's failure status.
// Every createJob error path calls fail() ONCE instead of hand-repeating
// the release order (idempotency scope + token + failed status) — a missed
// release was a leak class (the cleanup comments in createJob predate this).
type jobReservation struct {
	server           *Server
	scope            idempotency.Scope
	jobID            string
	tenantID         string
	target           string
	appID            string
	tokenAcquired    bool
	tokenMarkedToJob bool
}

// newJobReservation tracks an idempotency scope before the job exists.
func (s *Server) newJobReservation(scope idempotency.Scope, tenantID, target, appID string) *jobReservation {
	return &jobReservation{server: s, scope: scope, tenantID: tenantID, target: target, appID: appID}
}

// attachJob records the job ID and that a concurrency token was acquired
// (released per-job from here on).
func (res *jobReservation) attachJob(jobID string) {
	res.jobID = jobID
	res.tokenAcquired = true
	res.tokenMarkedToJob = true
}

// release frees the idempotency scope and the pre-job concurrency token.
// Used when the job was never created.
func (res *jobReservation) release(ctx context.Context) {
	_ = res.server.idempotency.Release(ctx, res.scope)
	if res.tokenAcquired && !res.tokenMarkedToJob {
		res.server.releaseConcurrencyToken(res.tenantID, res.target, res.appID)
	}
}

// fail marks the job failed_retryable, releases the idempotency scope, and
// releases the job-associated concurrency token — the exact order every
// error path must honor, in one place.
func (res *jobReservation) fail(ctx context.Context) {
	if res.jobID != "" {
		_, _, _ = res.server.jobs.UpdateStatus(ctx, res.jobID, jobstore.StatusFailedRetryable)
		res.server.releaseConcurrencyTokenForJob(res.jobID)
	} else if res.tokenAcquired {
		res.server.releaseConcurrencyToken(res.tenantID, res.target, res.appID)
	}
	_ = res.server.idempotency.Release(ctx, res.scope)
}
