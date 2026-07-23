package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PostgresStore struct {
	db           *sql.DB
	now          func() time.Time
	waitInterval time.Duration
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{
		db:           db,
		now:          time.Now,
		waitInterval: 300 * time.Millisecond,
	}
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
		Status:         StatusQueued,
		TraceID:        request.TraceID,
		RetryOf:        request.RetryOf,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if request.AwaitingAttachments {
		job.Status = StatusCreated
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
	status, result_json, trace_id, retry_of, event_sequence, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, nullif($5, ''), $6, $7,
	$8, nullif($9, ''), nullif($10, ''), $11, $12, $13, $14,
	$15, NULL, nullif($16, ''), nullif($17, ''), 1, $18, $19
)`,
		job.ID, job.APIVersion, job.TenantID, job.AppID, job.IdempotencyKey, job.Target, job.CommandType,
		clientJSON, job.ConversationID, job.TemplateID, inputJSON, optionsJSON, callbacksJSON, contextJSON,
		string(job.Status), job.TraceID, job.RetryOf, job.CreatedAt, job.UpdatedAt)
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
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_jobs SET status = $1, event_sequence = $2, updated_at = $3 WHERE id = $4`, string(job.Status), sequence, job.UpdatedAt, job.ID); err != nil {
		return Job{}, false, err
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
	query += " ORDER BY created_at ASC, id ASC"

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
		interval = 300 * time.Millisecond
	}
	for {
		events, found, err := p.listEvents(ctx, jobID, afterSequence, limit)
		if err != nil || !found || len(events) > 0 {
			return events, found, err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, true, ctx.Err()
		case <-timer.C:
		}
	}
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
	return job, true, nil
}

func (p *PostgresStore) ApplyWorkerEvent(ctx context.Context, event WorkerEvent) (Job, bool, error) {
	if p == nil || p.db == nil {
		return Job{}, false, fmt.Errorf("postgres job store is not configured")
	}
	if event.JobID == "" {
		return Job{}, false, fmt.Errorf("worker event job_id is required")
	}
	if event.Type == "" {
		return Job{}, false, fmt.Errorf("worker event type is required")
	}
	if !knownWorkerEventType(event.Type) {
		return Job{}, false, fmt.Errorf("worker event type %q is not supported", event.Type)
	}
	if event.APIVersion == "" {
		return Job{}, false, fmt.Errorf("worker event api_version is required")
	}
	if event.TraceID == "" {
		return Job{}, false, fmt.Errorf("worker event trace_id is required")
	}
	eventKey := workerEventKey(event)
	if eventKey == "" {
		return Job{}, false, fmt.Errorf("worker event must include event_id or positive sequence")
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
	if event.APIVersion != job.APIVersion {
		return job, true, fmt.Errorf("worker event api_version %q does not match job api_version %q", event.APIVersion, job.APIVersion)
	}
	if job.TraceID != "" && event.TraceID != job.TraceID {
		return job, true, fmt.Errorf("worker event trace_id %q does not match job trace_id %q", event.TraceID, job.TraceID)
	}

	var insertedKey string
	err = tx.QueryRowContext(ctx, `
INSERT INTO gateway_job_worker_event_keys (job_id, event_key, created_at)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
RETURNING event_key`, job.ID, eventKey, p.now().UTC()).Scan(&insertedKey)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return job, true, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	if TerminalStatus(job.Status) {
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return job, true, nil
	}

	if err := validateWorkerEventData(event.Type, event.Data); err != nil {
		return job, true, err
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
		return Job{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_jobs SET status = $1, result_json = $2, event_sequence = $3, updated_at = $4 WHERE id = $5`, string(job.Status), resultJSON, sequence, job.UpdatedAt, job.ID); err != nil {
		return Job{}, false, err
	}
	if err := insertEvent(ctx, tx, job, sequence, event.Type, data, p.now().UTC()); err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres job store is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return err
	}
	for _, objectName := range []string{
		"gateway_job_id_seq",
		"gateway_jobs",
		"gateway_job_events",
		"gateway_job_worker_event_keys",
	} {
		if err := requirePostgresObject(ctx, p.db, objectName); err != nil {
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
status, result_json, coalesce(trace_id, ''), coalesce(retry_of, ''), created_at, updated_at, event_sequence
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
	err := row.Scan(
		&job.ID, &job.APIVersion, &job.TenantID, &job.AppID, &job.IdempotencyKey, &job.Target, &job.CommandType,
		&clientJSON, &job.ConversationID, &job.TemplateID, &inputJSON, &optionsJSON, &callbacksJSON, &contextJSON,
		&status, &resultJSON, &job.TraceID, &job.RetryOf, &job.CreatedAt, &job.UpdatedAt, &sequence,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, 0, false, nil
	}
	if err != nil {
		return Job{}, 0, false, err
	}
	job.Status = Status(status)
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

func requirePostgresObject(ctx context.Context, db *sql.DB, objectName string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, objectName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing", objectName)
	}
	return nil
}
