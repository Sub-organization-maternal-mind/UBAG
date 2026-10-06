package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

var _ AttemptStore = (*PostgresStore)(nil)

// Locking (no cycles): BeginAttempt and CommitEvents take the job row first
// and the attempt rows after; RenewAttempt, MarkSubmitted and ExpireAttempts
// only ever touch attempt rows (ExpireAttempts with SKIP LOCKED, so it never
// waits). Lease times use the gateway clock (p.now), like the other stores:
// replicas are expected to share NTP-synced time; a skew of a few seconds only
// shifts when a lapsed lease may be superseded.

// EnableAttempts makes Ready require the gateway_job_attempts table
// (migrations/postgres/0022). Called at startup only when
// UBAG_EXECUTOR_ATTEMPTS is on, so the table stays optional while the flag is
// off.
func (p *PostgresStore) EnableAttempts() { p.attempts = true }

const attemptColumns = `job_id, attempt_id, generation, node_id, state, lease_expires_at,
input_fingerprint, workload_version, submitted_at, created_at, updated_at, ended_at`

func scanAttempt(row jobScanner) (Attempt, error) {
	var a Attempt
	var generation int64
	var state string
	var submitted, ended sql.NullTime
	if err := row.Scan(&a.JobID, &a.AttemptID, &generation, &a.NodeID, &state, &a.LeaseExpiresAt,
		&a.InputFingerprint, &a.WorkloadVersion, &submitted, &a.CreatedAt, &a.UpdatedAt, &ended); err != nil {
		return Attempt{}, err
	}
	a.Generation, a.State = uint64(generation), AttemptState(state)
	a.LeaseExpiresAt, a.CreatedAt, a.UpdatedAt = a.LeaseExpiresAt.UTC(), a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	if submitted.Valid {
		t := submitted.Time.UTC()
		a.SubmittedAt = &t
	}
	if ended.Valid {
		t := ended.Time.UTC()
		a.EndedAt = &t
	}
	return a, nil
}

func scanAttempts(rows *sql.Rows) ([]Attempt, error) {
	defer rows.Close()
	attempts := []Attempt{}
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}

// loadAttemptTx reads one attempt and, with lock, row-locks it. found=false
// when there is none.
func loadAttemptTx(ctx context.Context, tx *sql.Tx, ref AttemptRef, lock bool) (Attempt, bool, error) {
	query := `SELECT ` + attemptColumns + ` FROM gateway_job_attempts WHERE job_id = $1 AND attempt_id = $2`
	if lock {
		query += ` FOR UPDATE`
	}
	a, err := scanAttempt(tx.QueryRowContext(ctx, query, ref.JobID, ref.AttemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, false, nil
	}
	return a, err == nil, err
}

func (p *PostgresStore) BeginAttempt(ctx context.Context, request BeginAttemptRequest) (Attempt, error) {
	if p == nil || p.db == nil {
		return Attempt{}, fmt.Errorf("postgres job store is not configured")
	}
	ttl, err := request.validate()
	if err != nil {
		return Attempt{}, err
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, err
	}
	defer rollbackUnlessCommitted(tx)

	// The job row lock serializes every BeginAttempt/CommitEvents for this job,
	// which is what makes the generation compare-and-set below race free.
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM gateway_jobs WHERE id = $1 FOR UPDATE`, request.JobID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, fmt.Errorf("%w: job %s", ErrAttemptNotFound, request.JobID)
	}
	if err != nil {
		return Attempt{}, err
	}
	if TerminalStatus(Status(status)) {
		return Attempt{}, ErrAttemptJobTerminal
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+attemptColumns+` FROM gateway_job_attempts WHERE job_id = $1 ORDER BY generation ASC`, request.JobID)
	if err != nil {
		return Attempt{}, err
	}
	history, err := scanAttempts(rows)
	if err != nil {
		return Attempt{}, err
	}

	now := p.now().UTC()
	plan, err := planBegin(request, ttl, history, now)
	if err != nil {
		return Attempt{}, err
	}
	if plan.replay {
		return plan.attempt, tx.Commit()
	}
	if plan.expire != "" {
		if _, err := tx.ExecContext(ctx, `
UPDATE gateway_job_attempts SET state = 'expired', ended_at = $1, updated_at = $1
WHERE job_id = $2 AND attempt_id = $3 AND state = 'active'`, now, request.JobID, plan.expire); err != nil {
			return Attempt{}, err
		}
	}
	a := plan.attempt
	if _, err := tx.ExecContext(ctx, `
INSERT INTO gateway_job_attempts (`+attemptColumns+`)
VALUES ($1, $2, $3, $4, 'active', $5, $6, $7, NULL, $8, $8, NULL)`,
		a.JobID, a.AttemptID, int64(a.Generation), a.NodeID, a.LeaseExpiresAt, a.InputFingerprint, a.WorkloadVersion, a.CreatedAt); err != nil {
		return Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, err
	}
	return a, nil
}

// updateActiveAttempt runs one guarded UPDATE ... RETURNING against ref's
// active attempt at its generation (args $1..$3 are job, attempt, generation).
// When it matches nothing it reads the row back to report why, through the same
// fence rule the other stores use.
func (p *PostgresStore) updateActiveAttempt(ctx context.Context, ref AttemptRef, set string, args ...any) (Attempt, error) {
	if p == nil || p.db == nil {
		return Attempt{}, fmt.Errorf("postgres job store is not configured")
	}
	all := append([]any{ref.JobID, ref.AttemptID, int64(ref.Generation)}, args...)
	a, err := scanAttempt(p.db.QueryRowContext(ctx,
		`UPDATE gateway_job_attempts SET `+set+` WHERE job_id = $1 AND attempt_id = $2 AND generation = $3 AND state = 'active' RETURNING `+attemptColumns,
		all...))
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, err
	}
	current, err := scanAttempt(p.db.QueryRowContext(ctx,
		`SELECT `+attemptColumns+` FROM gateway_job_attempts WHERE job_id = $1 AND attempt_id = $2`, ref.JobID, ref.AttemptID))
	found := !errors.Is(err, sql.ErrNoRows)
	if err != nil && found {
		return Attempt{}, err
	}
	if fence := fenceAttempt(ref, current, found, false); fence != nil {
		return Attempt{}, fence
	}
	return Attempt{}, ErrAttemptInactive // unreachable: states only move forward
}

func (p *PostgresStore) RenewAttempt(ctx context.Context, ref AttemptRef, ttl time.Duration) (Attempt, error) {
	if err := ref.validate(); err != nil {
		return Attempt{}, err
	}
	ttl, err := attemptTTL(ttl)
	if err != nil {
		return Attempt{}, err
	}
	now := p.now().UTC()
	return p.updateActiveAttempt(ctx, ref,
		`lease_expires_at = GREATEST(lease_expires_at, $4::timestamptz), updated_at = $5`, now.Add(ttl), now)
}

func (p *PostgresStore) MarkSubmitted(ctx context.Context, ref AttemptRef) (Attempt, error) {
	if err := ref.validate(); err != nil {
		return Attempt{}, err
	}
	return p.updateActiveAttempt(ctx, ref,
		`submitted_at = COALESCE(submitted_at, $4::timestamptz), updated_at = $4::timestamptz`, p.now().UTC())
}

func (p *PostgresStore) CommitEvents(ctx context.Context, ref AttemptRef, events []WorkerEvent) (Job, error) {
	if p == nil || p.db == nil {
		return Job{}, fmt.Errorf("postgres job store is not configured")
	}
	if err := validateAttemptCommit(ref, events); err != nil {
		return Job{}, err
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer rollbackUnlessCommitted(tx)

	job, sequence, found, err := p.getJobForUpdate(ctx, tx, ref.JobID)
	if err != nil {
		return Job{}, err
	}
	if !found {
		return Job{}, fmt.Errorf("%w: job %s", ErrAttemptNotFound, ref.JobID)
	}
	attempt, found, err := loadAttemptTx(ctx, tx, ref, true)
	if err != nil {
		return Job{}, err
	}
	if err := fenceAttempt(ref, attempt, found, true); err != nil {
		return Job{}, err
	}

	// One transaction: any failing event rolls the whole batch back.
	changed := false
	for _, event := range events {
		var eventChanged bool
		if job, sequence, eventChanged, err = p.applyWorkerEventTx(ctx, tx, job, sequence, event); err != nil {
			return Job{}, err
		}
		changed = changed || eventChanged
	}
	if attempt.State == AttemptActive && TerminalStatus(job.Status) {
		now := p.now().UTC()
		if _, err := tx.ExecContext(ctx, `
UPDATE gateway_job_attempts SET state = 'finished', ended_at = $1, updated_at = $1
WHERE job_id = $2 AND attempt_id = $3`, now, ref.JobID, ref.AttemptID); err != nil {
			return Job{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	if changed {
		p.wake.notify(job.ID)
	}
	return job, nil
}

func (p *PostgresStore) ExpireAttempts(ctx context.Context, limit int) ([]Attempt, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("postgres job store is not configured")
	}
	now := p.now().UTC()
	rows, err := p.db.QueryContext(ctx, `
UPDATE gateway_job_attempts SET state = 'expired', ended_at = $1, updated_at = $1
WHERE (job_id, attempt_id) IN (
	SELECT job_id, attempt_id FROM gateway_job_attempts
	WHERE state = 'active' AND lease_expires_at <= $1
	ORDER BY lease_expires_at, job_id, attempt_id
	LIMIT $2
	FOR UPDATE SKIP LOCKED)
AND state = 'active'
RETURNING `+attemptColumns, now, expireBatch(limit))
	if err != nil {
		return nil, err
	}
	expired, err := scanAttempts(rows)
	if err != nil {
		return nil, err
	}
	sort.Slice(expired, func(i, j int) bool { // RETURNING order is unspecified
		if !expired[i].LeaseExpiresAt.Equal(expired[j].LeaseExpiresAt) {
			return expired[i].LeaseExpiresAt.Before(expired[j].LeaseExpiresAt)
		}
		if expired[i].JobID != expired[j].JobID {
			return expired[i].JobID < expired[j].JobID
		}
		return expired[i].AttemptID < expired[j].AttemptID
	})
	return expired, nil
}

func (p *PostgresStore) ListAttempts(ctx context.Context, jobID string) ([]Attempt, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("postgres job store is not configured")
	}
	rows, err := p.db.QueryContext(ctx, `SELECT `+attemptColumns+` FROM gateway_job_attempts WHERE job_id = $1 ORDER BY generation ASC`, jobID)
	if err != nil {
		return nil, err
	}
	return scanAttempts(rows)
}
