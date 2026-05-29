package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sqliteTimeLayout stores timestamps as fixed-width millisecond RFC3339 UTC
// strings so that lexical ordering matches chronological ordering in SQLite.
const sqliteTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// SQLiteStore is a jobs.Store backed by a SQLite database. It mirrors
// PostgresStore exactly, including the optional MetricsStore, ScopedStore and
// EventLister interfaces. JSON map fields are stored as TEXT JSON.
type SQLiteStore struct {
	db           *sql.DB
	now          func() time.Time
	waitInterval time.Duration
}

func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{
		db:           db,
		now:          time.Now,
		waitInterval: 300 * time.Millisecond,
	}
}

func (s *SQLiteStore) Create(ctx context.Context, request CreateRequest) (Job, error) {
	if s == nil || s.db == nil {
		return Job{}, fmt.Errorf("sqlite job store is not configured")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer rollbackUnlessCommitted(tx)

	result, err := tx.ExecContext(ctx, `INSERT INTO gateway_job_id_seq DEFAULT VALUES`)
	if err != nil {
		return Job{}, err
	}
	numericID, err := result.LastInsertId()
	if err != nil {
		return Job{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_job_id_seq WHERE seq = ?`, numericID); err != nil {
		return Job{}, err
	}

	now := s.now().UTC()
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
	?, ?, ?, ?, nullif(?, ''), ?, ?,
	?, nullif(?, ''), nullif(?, ''), ?, ?, ?, ?,
	?, NULL, nullif(?, ''), nullif(?, ''), 1, ?, ?
)`,
		job.ID, job.APIVersion, job.TenantID, job.AppID, job.IdempotencyKey, job.Target, job.CommandType,
		clientJSON, job.ConversationID, job.TemplateID, inputJSON, optionsJSON, callbacksJSON, contextJSON,
		string(job.Status), job.TraceID, job.RetryOf, formatSQLiteTime(job.CreatedAt), formatSQLiteTime(job.UpdatedAt))
	if err != nil {
		return Job{}, err
	}

	if err := insertSQLiteEvent(ctx, tx, job, 1, "queued", map[string]any{
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

func (s *SQLiteStore) Get(ctx context.Context, id string) (Job, bool, error) {
	if s == nil || s.db == nil {
		return Job{}, false, fmt.Errorf("sqlite job store is not configured")
	}
	job, found, err := scanSQLiteJob(s.db.QueryRowContext(ctx, selectSQLiteJobSQL()+` WHERE id = ?`, id))
	return job, found, err
}

func (s *SQLiteStore) GetScoped(ctx context.Context, id string, tenantID string, appID string) (Job, bool, error) {
	if s == nil || s.db == nil {
		return Job{}, false, fmt.Errorf("sqlite job store is not configured")
	}
	job, found, err := scanSQLiteJob(s.db.QueryRowContext(ctx, selectSQLiteJobSQL()+` WHERE id = ? AND tenant_id = ? AND app_id = ?`, id, tenantID, appID))
	return job, found, err
}

func (s *SQLiteStore) List(ctx context.Context, filter ListFilter) ([]Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite job store is not configured")
	}
	query := selectSQLiteJobSQL() + ` WHERE 1=1`
	args := []any{}
	addFilter := func(condition string, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		args = append(args, value)
		query += fmt.Sprintf(" AND %s = ?", condition)
	}
	addFilter("tenant_id", filter.TenantID)
	addFilter("app_id", filter.AppID)
	addFilter("status", filter.Status)
	addFilter("target", filter.Target)
	query += " ORDER BY created_at ASC, id ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []Job{}
	for rows.Next() {
		job, err := scanSQLiteJobFromRows(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *SQLiteStore) ListAllEvents(ctx context.Context, filter EventListFilter) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite job store is not configured")
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
		query += fmt.Sprintf(" AND %s = ?", condition)
	}
	addFilter("j.tenant_id", filter.TenantID)
	addFilter("j.app_id", filter.AppID)
	if strings.TrimSpace(filter.AfterEventID) != "" {
		args = append(args, filter.AfterEventID)
		query += " AND (e.created_at, e.id) > (SELECT created_at, id FROM gateway_job_events WHERE id = ?)"
	}
	args = append(args, limit)
	query += " ORDER BY e.created_at ASC, e.id ASC LIMIT ?"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		event, err := scanSQLiteEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *SQLiteStore) ListEvents(ctx context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("sqlite job store is not configured")
	}
	return s.listEvents(ctx, jobID, afterSequence, limit)
}

func (s *SQLiteStore) WaitEvents(ctx context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("sqlite job store is not configured")
	}
	interval := s.waitInterval
	if interval <= 0 {
		interval = 300 * time.Millisecond
	}
	for {
		events, found, err := s.listEvents(ctx, jobID, afterSequence, limit)
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

func (s *SQLiteStore) UpdateStatus(ctx context.Context, id string, status Status) (Job, bool, error) {
	if s == nil || s.db == nil {
		return Job{}, false, fmt.Errorf("sqlite job store is not configured")
	}
	if !KnownStatus(status) {
		return Job{}, false, fmt.Errorf("unknown job status %q", status)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer rollbackUnlessCommitted(tx)

	job, sequence, found, err := s.getJobForUpdate(ctx, tx, id)
	if err != nil || !found {
		return Job{}, found, err
	}
	if job.Status == status || TerminalStatus(job.Status) {
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return job, true, nil
	}

	now := s.now().UTC()
	sequence++
	job.Status = status
	job.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_jobs SET status = ?, event_sequence = ?, updated_at = ? WHERE id = ?`, string(job.Status), sequence, formatSQLiteTime(job.UpdatedAt), job.ID); err != nil {
		return Job{}, false, err
	}
	if err := insertSQLiteEvent(ctx, tx, job, sequence, string(status), map[string]any{
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

func (s *SQLiteStore) ApplyWorkerEvent(ctx context.Context, event WorkerEvent) (Job, bool, error) {
	if s == nil || s.db == nil {
		return Job{}, false, fmt.Errorf("sqlite job store is not configured")
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer rollbackUnlessCommitted(tx)

	job, sequence, found, err := s.getJobForUpdate(ctx, tx, event.JobID)
	if err != nil || !found {
		return Job{}, found, err
	}
	if event.APIVersion != job.APIVersion {
		return job, true, fmt.Errorf("worker event api_version %q does not match job api_version %q", event.APIVersion, job.APIVersion)
	}
	if job.TraceID != "" && event.TraceID != job.TraceID {
		return job, true, fmt.Errorf("worker event trace_id %q does not match job trace_id %q", event.TraceID, job.TraceID)
	}

	insertResult, err := tx.ExecContext(ctx, `
INSERT INTO gateway_job_worker_event_keys (job_id, event_key, created_at)
VALUES (?, ?, ?)
ON CONFLICT DO NOTHING`, job.ID, eventKey, formatSQLiteTime(s.now().UTC()))
	if err != nil {
		return Job{}, false, err
	}
	if inserted, _ := insertResult.RowsAffected(); inserted == 0 {
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return job, true, nil
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
	job.UpdatedAt = s.now().UTC()
	sequence++

	resultJSON, err := marshalNullableJSON(job.Result)
	if err != nil {
		return Job{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_jobs SET status = ?, result_json = ?, event_sequence = ?, updated_at = ? WHERE id = ?`, string(job.Status), resultJSON, sequence, formatSQLiteTime(job.UpdatedAt), job.ID); err != nil {
		return Job{}, false, err
	}
	if err := insertSQLiteEvent(ctx, tx, job, sequence, event.Type, data, s.now().UTC()); err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite job store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	for _, objectName := range []string{
		"gateway_job_id_seq",
		"gateway_jobs",
		"gateway_job_events",
		"gateway_job_worker_event_keys",
	} {
		if err := requireSQLiteObject(ctx, s.db, objectName); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) CountsByStatus(ctx context.Context, filter ListFilter) (map[Status]int, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("sqlite job store is not configured")
	}
	query := `SELECT status, count(*) FROM gateway_jobs WHERE 1=1`
	args := []any{}
	addFilter := func(condition string, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		args = append(args, value)
		query += fmt.Sprintf(" AND %s = ?", condition)
	}
	addFilter("tenant_id", filter.TenantID)
	addFilter("app_id", filter.AppID)
	addFilter("status", filter.Status)
	addFilter("target", filter.Target)
	query += " GROUP BY status"

	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *SQLiteStore) listEvents(ctx context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_jobs WHERE id = ?)`, jobID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_id, api_version, type, sequence, data_json, trace_id, created_at
FROM gateway_job_events
WHERE job_id = ? AND sequence > ?
ORDER BY sequence ASC
LIMIT ?`, jobID, afterSequence, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		event, err := scanSQLiteEvent(rows)
		if err != nil {
			return nil, false, err
		}
		events = append(events, event)
	}
	return events, true, rows.Err()
}

func (s *SQLiteStore) getJobForUpdate(ctx context.Context, tx *sql.Tx, id string) (Job, int, bool, error) {
	row := tx.QueryRowContext(ctx, selectSQLiteJobSQL()+` WHERE id = ?`, id)
	job, sequence, found, err := scanSQLiteJobWithSequence(row)
	return job, sequence, found, err
}

func insertSQLiteEvent(ctx context.Context, tx *sql.Tx, job Job, sequence int, eventType string, data map[string]any, now time.Time) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	eventID := fmt.Sprintf("evt_%s%03d", strings.TrimPrefix(job.ID, "job_"), sequence)
	_, err = tx.ExecContext(ctx, `
INSERT INTO gateway_job_events (id, job_id, api_version, type, sequence, data_json, trace_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, nullif(?, ''), ?)`,
		eventID, job.ID, job.APIVersion, eventType, sequence, string(payload), job.TraceID, formatSQLiteTime(now))
	return err
}

func selectSQLiteJobSQL() string {
	return `SELECT id, api_version, tenant_id, app_id, coalesce(idempotency_key, ''), target, command_type,
client_json, coalesce(conversation_id, ''), coalesce(template_id, ''), input_json, options_json, callbacks_json, context_json,
status, result_json, coalesce(trace_id, ''), coalesce(retry_of, ''), created_at, updated_at, event_sequence
FROM gateway_jobs`
}

func scanSQLiteJob(row jobScanner) (Job, bool, error) {
	job, _, found, err := scanSQLiteJobWithSequence(row)
	return job, found, err
}

func scanSQLiteJobWithSequence(row jobScanner) (Job, int, bool, error) {
	var job Job
	var clientJSON, inputJSON, optionsJSON, callbacksJSON, contextJSON, resultJSON []byte
	var status string
	var createdAt, updatedAt string
	var sequence int
	err := row.Scan(
		&job.ID, &job.APIVersion, &job.TenantID, &job.AppID, &job.IdempotencyKey, &job.Target, &job.CommandType,
		&clientJSON, &job.ConversationID, &job.TemplateID, &inputJSON, &optionsJSON, &callbacksJSON, &contextJSON,
		&status, &resultJSON, &job.TraceID, &job.RetryOf, &createdAt, &updatedAt, &sequence,
	)
	if err == sql.ErrNoRows {
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
	job.CreatedAt = parseSQLiteTime(createdAt)
	job.UpdatedAt = parseSQLiteTime(updatedAt)
	return job, sequence, true, nil
}

func scanSQLiteJobFromRows(rows *sql.Rows) (Job, error) {
	job, _, _, err := scanSQLiteJobWithSequence(rows)
	return job, err
}

func scanSQLiteEvent(rows *sql.Rows) (Event, error) {
	var event Event
	var data []byte
	var traceID sql.NullString
	var createdAt string
	if err := rows.Scan(&event.ID, &event.JobID, &event.APIVersion, &event.Type, &event.Sequence, &data, &traceID, &createdAt); err != nil {
		return Event{}, err
	}
	event.Data = decodeMap(data)
	event.TraceID = traceID.String
	event.CreatedAt = parseSQLiteTime(createdAt)
	return event, nil
}

func requireSQLiteObject(ctx context.Context, db *sql.DB, objectName string) error {
	var name string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE name = ? LIMIT 1`, objectName).Scan(&name)
	if err == sql.ErrNoRows {
		return fmt.Errorf("%s is missing", objectName)
	}
	if err != nil {
		return err
	}
	return nil
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}

func parseSQLiteTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{sqliteTimeLayout, time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
