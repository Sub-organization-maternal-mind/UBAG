package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ubag/ubag/apps/gateway/internal/storekit"
	"strings"
	"time"
)

type PostgresStore struct {
	db           *sql.DB
	now          func() time.Time
	waitInterval time.Duration
	wake         *eventHub     // nil = legacy fixed-interval poll (UBAG_EVENT_NOTIFY=off)
	wakeFallback time.Duration // fallback poll cadence while wake != nil
	attempts     bool          // UBAG_EXECUTOR_ATTEMPTS: Ready also requires gateway_job_attempts
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{
		db:           db,
		now:          time.Now,
		waitInterval: defaultWaitEventsInterval,
	}
}

// EnableEventNotify turns on the in-process per-job wake hub: WaitEvents then
// re-reads on a commit notification and otherwise only every fallback. The hub
// only sees this process's writes, so the fallback also covers other gateways
// sharing the database. Call once at startup, before the store serves requests.
func (p *PostgresStore) EnableEventNotify(fallback time.Duration) {
	if fallback <= 0 {
		fallback = DefaultEventFallbackInterval
	}
	p.wake, p.wakeFallback = newEventHub(), fallback
}

// SubscribeJobWake implements JobWaker (ok=false while the hub is off).
func (p *PostgresStore) SubscribeJobWake(jobID string) (<-chan struct{}, func(), bool) {
	if p == nil || p.wake == nil {
		return nil, nil, false
	}
	ch, cancel := p.wake.subscribe(jobID)
	return ch, cancel, true
}

func (p *PostgresStore) Create(ctx context.Context, request CreateRequest) (Job, error) {
	if p == nil || p.db == nil {
		return Job{}, fmt.Errorf("postgres job store is not configured")
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer rollbackUnlessCommitted(tx)

	var numericID int64
	if err := tx.QueryRowContext(ctx, `SELECT nextval('gateway_job_id_seq')`).Scan(&numericID); err != nil {
		return Job{}, err
	}

	now := p.now().UTC()
	status := StatusQueued
	if request.NotBefore != nil && request.NotBefore.After(now) {
		status = StatusScheduled
	}
	if request.AwaitingAttachments {
		status = StatusCreated
	}
	job := Job{
		ID:             fmt.Sprintf("job_%012d", numericID),
		APIVersion:     request.APIVersion,
		TenantID:       request.TenantID,
		AppID:          request.AppID,
		IdempotencyKey: request.IdempotencyKey,
		Target:         request.Target,
		CommandType:    request.CommandType,
		Client:         cloneMap(request.Client),
		ConversationID: request.ConversationID,
		TemplateID:     request.TemplateID,
		Input:          cloneMap(request.Input),
		Options:        cloneMap(request.Options),
		Callbacks:      cloneMap(request.Callbacks),
		Context:        cloneMap(request.Context),
		Status:         status,
		TraceID:        request.TraceID,
		RetryOf:        request.RetryOf,
		NotBefore:      request.NotBefore,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	clientJSON, err := marshalNullableJSON(job.Client)
	if err != nil {
		return Job{}, err
	}
	inputJSON, err := marshalNullableJSON(job.Input)
	if err != nil {
		return Job{}, err
	}
	optionsJSON, err := marshalNullableJSON(job.Options)
	if err != nil {
		return Job{}, err
	}
	callbacksJSON, err := marshalNullableJSON(job.Callbacks)
	if err != nil {
		return Job{}, err
	}
	contextJSON, err := marshalNullableJSON(job.Context)
	if err != nil {
		return Job{}, err
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO gateway_jobs (
	id, api_version, tenant_id, app_id, idempotency_key, target, command_type,
	client_json, conversation_id, template_id, input_json, options_json, callbacks_json, context_json,
	status, result_json, trace_id, retry_of, event_sequence, created_at, updated_at, not_before
) VALUES (
	$1, $2, $3, $4, nullif($5, ''), $6, $7,
	$8, nullif($9, ''), nullif($10, ''), $11, $12, $13, $14,
	$15, NULL, nullif($16, ''), nullif($17, ''), 1, $18, $19, $20
)`,
		job.ID, job.APIVersion, job.TenantID, job.AppID, job.IdempotencyKey, job.Target, job.CommandType,
		clientJSON, job.ConversationID, job.TemplateID, inputJSON, optionsJSON, callbacksJSON, contextJSON,
		string(job.Status), job.TraceID, job.RetryOf, job.CreatedAt, job.UpdatedAt, nullableTime(job.NotBefore))
	if err != nil {
		return Job{}, err
	}

	initialEvent := "queued"
	if job.Status == StatusCreated {
		initialEvent = "created"
	}
	if err := insertEvent(ctx, tx, job, 1, initialEvent, map[string]any{
		"status":       string(job.Status),
		"target":       job.Target,
		"command_type": job.CommandType,
	}, now); err != nil {
		return Job{}, err
	}

	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	p.wake.notify(job.ID)
	return job, nil
}

// TransitionStatus atomically moves job `id` from `from` to `to` iff its current
// status equals `from` (SELECT ... FOR UPDATE serializes concurrent callers). See
// jobs.Store.
func (p *PostgresStore) TransitionStatus(ctx context.Context, id string, from Status, to Status) (Job, bool, error) {
	if p == nil || p.db == nil {
		return Job{}, false, fmt.Errorf("postgres job store is not configured")
	}
	if !KnownStatus(to) {
		return Job{}, false, fmt.Errorf("unknown job status %q", to)
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer rollbackUnlessCommitted(tx)

	job, sequence, found, err := p.getJobForUpdate(ctx, tx, id)
	if err != nil || !found {
		return Job{}, found, err
	}
	if job.Status != from {
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return job, false, nil
	}

	now := p.now().UTC()
	sequence++
	job.Status = to
	job.UpdatedAt = now
	// Compare-and-set: the WHERE re-checks the status so a concurrent winner
	// (FOR UPDATE serializes the read, but the guard also defends against
	// read-committed surprises and makes the lost update impossible) can never
	// have a stale caller overwrite a status that already moved on — including
	// a terminal one. RowsAffected is the authoritative win/lose signal.
	result, err := tx.ExecContext(ctx, `UPDATE gateway_jobs SET status = $1, event_sequence = $2, updated_at = $3 WHERE id = $4 AND status = $5`, string(job.Status), sequence, job.UpdatedAt, job.ID, string(from))
	if err != nil {
		return Job{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Job{}, false, err
	}
	if affected == 0 {
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		current, found, err := p.Get(ctx, id)
		if err != nil {
			return Job{}, false, err
		}
		if !found {
			return Job{}, false, nil
		}
		return current, false, fmt.Errorf("%w: job %s is no longer %s", ErrConflict, id, from)
	}
	if err := insertEvent(ctx, tx, job, sequence, string(to), map[string]any{
		"status": string(to),
		"target": job.Target,
	}, now); err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	p.wake.notify(job.ID)
	return job, true, nil
}

func (p *PostgresStore) Get(ctx context.Context, id string) (Job, bool, error) {
	if p == nil || p.db == nil {
		return Job{}, false, fmt.Errorf("postgres job store is not configured")
	}
	job, found, err := scanJob(p.db.QueryRowContext(ctx, selectJobSQL()+` WHERE id = $1`, id))
	return job, found, err
}

func (p *PostgresStore) GetScoped(ctx context.Context, id string, tenantID string, appID string) (Job, bool, error) {
	if p == nil || p.db == nil {
		return Job{}, false, fmt.Errorf("postgres job store is not configured")
	}
	job, found, err := scanJob(p.db.QueryRowContext(ctx, selectJobSQL()+` WHERE id = $1 AND tenant_id = $2 AND app_id = $3`, id, tenantID, appID))
	return job, found, err
}

func (p *PostgresStore) List(ctx context.Context, filter ListFilter) ([]Job, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("postgres job store is not configured")
	}
	query := selectJobSQL() + ` WHERE 1=1`
	args := []any{}
	addFilter := func(condition string, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		args = append(args, value)
		query += fmt.Sprintf(" AND %s = $%d", condition, len(args))
	}
	addFilter("tenant_id", filter.TenantID)
	addFilter("app_id", filter.AppID)
	addFilter("status", filter.Status)
	addFilter("target", filter.Target)
	// Cursor pagination is resolved store-side with a row-value tuple compare
	// (same shape as ListAllEvents' AfterEventID) so a list route never has to
	// load the full table to locate the cursor position.
	if strings.TrimSpace(filter.AfterID) != "" {
		operator := ">"
		if filter.Descending {
			operator = "<"
		}
		args = append(args, filter.AfterID)
		query += fmt.Sprintf(" AND (created_at, id) %s (SELECT created_at, id FROM gateway_jobs WHERE id = $%d)", operator, len(args))
	}
	if filter.Descending {
		query += " ORDER BY created_at DESC, id DESC"
	} else {
		query += " ORDER BY created_at ASC, id ASC"
	}
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []Job{}
	for rows.Next() {
		job, err := scanJobFromRows(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (p *PostgresStore) ListAllEvents(ctx context.Context, filter EventListFilter) ([]Event, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("postgres job store is not configured")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	query := `
SELECT e.id, e.job_id, e.api_version, e.type, e.sequence, e.data_json, e.trace_id, e.created_at
FROM gateway_job_events e
JOIN gateway_jobs j ON j.id = e.job_id
WHERE 1=1`
	args := []any{}
	addFilter := func(condition string, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		args = append(args, value)
		query += fmt.Sprintf(" AND %s = $%d", condition, len(args))
	}
	addFilter("j.tenant_id", filter.TenantID)
	addFilter("j.app_id", filter.AppID)
	if strings.TrimSpace(filter.AfterEventID) != "" {
		args = append(args, filter.AfterEventID)
		query += fmt.Sprintf(" AND (e.created_at, e.id) > (SELECT created_at, id FROM gateway_job_events WHERE id = $%d)", len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY e.created_at ASC, e.id ASC LIMIT $%d", len(args))

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		var data []byte
		if err := rows.Scan(&event.ID, &event.JobID, &event.APIVersion, &event.Type, &event.Sequence, &data, &event.TraceID, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.Data = decodeMap(data)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (p *PostgresStore) ListEvents(ctx context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error) {
	if p == nil || p.db == nil {
		return nil, false, fmt.Errorf("postgres job store is not configured")
	}
	return p.listEvents(ctx, jobID, afterSequence, limit)
}

func (p *PostgresStore) WaitEvents(ctx context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error) {
	if p == nil || p.db == nil {
		return nil, false, fmt.Errorf("postgres job store is not configured")
	}
	interval := p.waitInterval
	if interval <= 0 {
		interval = defaultWaitEventsInterval
	}
	return waitEventsLoop(ctx, p.wake, p.wakeFallback, interval, jobID, func() ([]Event, bool, error) {
		return p.listEvents(ctx, jobID, afterSequence, limit)
	})
}

func (p *PostgresStore) UpdateStatus(ctx context.Context, id string, status Status) (Job, bool, error) {
	if p == nil || p.db == nil {
		return Job{}, false, fmt.Errorf("postgres job store is not configured")
	}
	if !KnownStatus(status) {
		return Job{}, false, fmt.Errorf("unknown job status %q", status)
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer rollbackUnlessCommitted(tx)

	job, sequence, found, err := p.getJobForUpdate(ctx, tx, id)
	if err != nil || !found {
		return Job{}, found, err
	}
	if job.Status == status || TerminalStatus(job.Status) {
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return job, true, nil
	}
	// The API mutation path honors the same transition validation as the
	// worker-event path: never backwards, never out of a terminal status.
	if !shouldAdvanceStatus(job.Status, status) {
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return job, true, nil
	}

	now := p.now().UTC()
	sequence++
	job.Status = status
	job.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_jobs SET status = $1, event_sequence = $2, updated_at = $3 WHERE id = $4`, string(job.Status), sequence, job.UpdatedAt, job.ID); err != nil {
		return Job{}, false, err
	}
	if err := insertEvent(ctx, tx, job, sequence, string(status), map[string]any{
		"status": string(status),
		"target": job.Target,
	}, now); err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	p.wake.notify(job.ID)
	return job, true, nil
}

func (p *PostgresStore) ApplyWorkerEvent(ctx context.Context, event WorkerEvent) (Job, bool, error) {
	if p == nil || p.db == nil {
		return Job{}, false, fmt.Errorf("postgres job store is not configured")
	}
	if err := checkWorkerEvent(event); err != nil {
		return Job{}, false, err
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer rollbackUnlessCommitted(tx)

	job, sequence, found, err := p.getJobForUpdate(ctx, tx, event.JobID)
	if err != nil || !found {
		return Job{}, found, err
	}
	job, _, changed, err := p.applyWorkerEventTx(ctx, tx, job, sequence, event)
	if err != nil {
		if job.ID == "" {
			return Job{}, false, err
		}
		return job, true, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	if changed {
		p.wake.notify(job.ID)
	}
	return job, true, nil
}

// ApplyWorkerEvents applies a batch of events for ONE job in a single
// transaction: one FOR UPDATE lock, one commit, and one wake of the job's
// waiters instead of one per event. Any failing event rolls the whole batch back.
func (p *PostgresStore) ApplyWorkerEvents(ctx context.Context, events []WorkerEvent) (Job, bool, error) {
	if p == nil || p.db == nil {
		return Job{}, false, fmt.Errorf("postgres job store is not configured")
	}
	if err := validateEventBatch(events); err != nil {
		return Job{}, false, err
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer rollbackUnlessCommitted(tx)

	job, sequence, found, err := p.getJobForUpdate(ctx, tx, events[0].JobID)
	if err != nil || !found {
		return Job{}, found, err
	}
	changed := false
	for _, event := range events {
		var eventChanged bool
		if job, sequence, eventChanged, err = p.applyWorkerEventTx(ctx, tx, job, sequence, event); err != nil {
			if job.ID == "" {
				return Job{}, false, err
			}
			return job, true, err
		}
		changed = changed || eventChanged
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	if changed {
		p.wake.notify(job.ID)
	}
	return job, true, nil
}

// applyWorkerEventTx applies one pre-checked event to the locked job row inside
// tx (no commit). It returns the job and event sequence as of the call, and
// whether the job changed (false for a duplicate event or a terminal job; the
// dedupe key is still recorded). On a validation error it returns the
// unchanged job; on a database error it returns the zero Job. Shared by
// ApplyWorkerEvent and CommitEvents so both apply events identically.
func (p *PostgresStore) applyWorkerEventTx(ctx context.Context, tx *sql.Tx, job Job, sequence int, event WorkerEvent) (Job, int, bool, error) {
	if err := checkWorkerEventAgainstJob(job, event); err != nil {
		return job, sequence, false, err
	}

	var insertedKey string
	err := tx.QueryRowContext(ctx, `
INSERT INTO gateway_job_worker_event_keys (job_id, event_key, created_at)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
RETURNING event_key`, job.ID, workerEventKey(event), p.now().UTC()).Scan(&insertedKey)
	if errors.Is(err, sql.ErrNoRows) {
		return job, sequence, false, nil
	}
	if err != nil {
		return Job{}, sequence, false, err
	}
	if TerminalStatus(job.Status) {
		return job, sequence, false, nil
	}

	if err := validateWorkerEventData(event.Type, event.Data); err != nil {
		return job, sequence, false, err
	}
	data, _ := sanitizeWorkerData(event.Type, event.Data).(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	metadata := map[string]any{
		"event_id": event.EventID,
		"sequence": event.Sequence,
		"type":     event.Type,
	}
	if !event.CreatedAt.IsZero() {
		metadata["created_at"] = event.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	data["worker_event"] = metadata

	nextStatus := statusFromWorkerEvent(event, job.Status)
	if result := resultFromWorkerEvent(event, data); result != nil {
		job.Result = result
	}
	if shouldAdvanceStatus(job.Status, nextStatus) {
		job.Status = nextStatus
	}
	job.UpdatedAt = p.now().UTC()
	sequence++

	resultJSON, err := marshalNullableJSON(job.Result)
	if err != nil {
		return Job{}, sequence, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_jobs SET status = $1, result_json = $2, event_sequence = $3, updated_at = $4 WHERE id = $5`, string(job.Status), resultJSON, sequence, job.UpdatedAt, job.ID); err != nil {
		return Job{}, sequence, false, err
	}
	if err := insertEvent(ctx, tx, job, sequence, event.Type, data, p.now().UTC()); err != nil {
		return Job{}, sequence, false, err
	}
	return job, sequence, true, nil
}

func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres job store is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return err
	}
	required := []string{
		"gateway_job_id_seq",
		"gateway_jobs",
		"gateway_job_events",
		"gateway_job_worker_event_keys",
	}
	if p.attempts {
		required = append(required, "gateway_job_attempts")
	}
	for _, objectName := range required {
		if err := storekit.RequirePostgresObject(ctx, p.db, objectName); err != nil {
			return err
		}
	}
	return nil
}

func (p *PostgresStore) CountsByStatus(ctx context.Context, filter ListFilter) (map[Status]int, int, error) {
	if p == nil || p.db == nil {
		return nil, 0, fmt.Errorf("postgres job store is not configured")
	}
	query := `SELECT status, count(*) FROM gateway_jobs WHERE 1=1`
	args := []any{}
	addFilter := func(condition string, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		args = append(args, value)
		query += fmt.Sprintf(" AND %s = $%d", condition, len(args))
	}
	addFilter("tenant_id", filter.TenantID)
	addFilter("app_id", filter.AppID)
	addFilter("status", filter.Status)
	addFilter("target", filter.Target)
	query += " GROUP BY status"

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	counts := map[Status]int{}
	total := 0
	for rows.Next() {
		var status Status
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, 0, err
		}
		counts[status] = count
		total += count
	}
	return counts, total, rows.Err()
}

func (p *PostgresStore) listEvents(ctx context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error) {
	var exists bool
	if err := p.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_jobs WHERE id = $1)`, jobID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.db.QueryContext(ctx, `
SELECT id, job_id, api_version, type, sequence, data_json, trace_id, created_at
FROM gateway_job_events
WHERE job_id = $1 AND sequence > $2
ORDER BY sequence ASC
LIMIT $3`, jobID, afterSequence, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		var event Event
		var data []byte
		if err := rows.Scan(&event.ID, &event.JobID, &event.APIVersion, &event.Type, &event.Sequence, &data, &event.TraceID, &event.CreatedAt); err != nil {
			return nil, false, err
		}
		event.Data = decodeMap(data)
		events = append(events, event)
	}
	return events, true, rows.Err()
}

// RecentEvents returns the newest `limit` events for a job in ascending
// sequence order, bounding the signal-reconstruction scan on hot status polls
// (see jobs.RecentEventLister). The caller has already loaded the job, so this
// skips the existence pre-check and reports found=true.
func (p *PostgresStore) RecentEvents(ctx context.Context, jobID string, limit int) ([]Event, bool, error) {
	if p == nil || p.db == nil {
		return nil, false, fmt.Errorf("postgres job store is not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.db.QueryContext(ctx, `
SELECT id, job_id, api_version, type, sequence, data_json, trace_id, created_at
FROM gateway_job_events
WHERE job_id = $1
ORDER BY sequence DESC
LIMIT $2`, jobID, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		var data []byte
		if err := rows.Scan(&event.ID, &event.JobID, &event.APIVersion, &event.Type, &event.Sequence, &data, &event.TraceID, &event.CreatedAt); err != nil {
			return nil, false, err
		}
		event.Data = decodeMap(data)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	reverseEvents(events)
	return events, true, nil
}

func (p *PostgresStore) getJobForUpdate(ctx context.Context, tx *sql.Tx, id string) (Job, int, bool, error) {
	row := tx.QueryRowContext(ctx, selectJobSQL()+` WHERE id = $1 FOR UPDATE`, id)
	job, sequence, found, err := scanJobWithSequence(row)
	return job, sequence, found, err
}

func insertEvent(ctx context.Context, tx *sql.Tx, job Job, sequence int, eventType string, data map[string]any, now time.Time) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	eventID := fmt.Sprintf("evt_%s%03d", strings.TrimPrefix(job.ID, "job_"), sequence)
	_, err = tx.ExecContext(ctx, `
INSERT INTO gateway_job_events (id, job_id, api_version, type, sequence, data_json, trace_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8)`,
		eventID, job.ID, job.APIVersion, eventType, sequence, payload, job.TraceID, now)
	return err
}

func selectJobSQL() string {
	return `SELECT id, api_version, tenant_id, app_id, coalesce(idempotency_key, ''), target, command_type,
client_json, coalesce(conversation_id, ''), coalesce(template_id, ''), input_json, options_json, callbacks_json, context_json,
status, result_json, coalesce(trace_id, ''), coalesce(retry_of, ''), created_at, updated_at, event_sequence, not_before
FROM gateway_jobs`
}

type jobScanner interface {
	Scan(dest ...any) error
}

func scanJob(row jobScanner) (Job, bool, error) {
	job, _, found, err := scanJobWithSequence(row)
	return job, found, err
}

func scanJobWithSequence(row jobScanner) (Job, int, bool, error) {
	var job Job
	var clientJSON, inputJSON, optionsJSON, callbacksJSON, contextJSON, resultJSON []byte
	var status string
	var sequence int
	var notBefore sql.NullTime
	err := row.Scan(
		&job.ID, &job.APIVersion, &job.TenantID, &job.AppID, &job.IdempotencyKey, &job.Target, &job.CommandType,
		&clientJSON, &job.ConversationID, &job.TemplateID, &inputJSON, &optionsJSON, &callbacksJSON, &contextJSON,
		&status, &resultJSON, &job.TraceID, &job.RetryOf, &job.CreatedAt, &job.UpdatedAt, &sequence, &notBefore,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, 0, false, nil
	}
	if err != nil {
		return Job{}, 0, false, err
	}
	job.Status = Status(status)
	if notBefore.Valid {
		t := notBefore.Time.UTC()
		job.NotBefore = &t
	}
	job.Client = decodeMap(clientJSON)
	job.Input = decodeMap(inputJSON)
	job.Options = decodeMap(optionsJSON)
	job.Callbacks = decodeMap(callbacksJSON)
	job.Context = decodeMap(contextJSON)
	job.Result = decodeAny(resultJSON)
	return job, sequence, true, nil
}

func scanJobFromRows(rows *sql.Rows) (Job, error) {
	job, _, _, err := scanJobWithSequence(rows)
	return job, err
}

func marshalNullableJSON(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// nullableTime maps an optional timestamp to a driver value: nil becomes SQL
// NULL so absent optionals never write a zero time.
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func decodeMap(payload []byte) map[string]any {
	if len(payload) == 0 || string(payload) == "null" {
		return nil
	}
	var output map[string]any
	if err := json.Unmarshal(payload, &output); err != nil {
		return nil
	}
	return output
}

func decodeAny(payload []byte) any {
	if len(payload) == 0 || string(payload) == "null" {
		return nil
	}
	var output any
	if err := json.Unmarshal(payload, &output); err != nil {
		return nil
	}
	return output
}

func rollbackUnlessCommitted(tx *sql.Tx) {
	_ = tx.Rollback()
}
