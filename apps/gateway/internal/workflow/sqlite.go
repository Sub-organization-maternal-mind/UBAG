package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sqliteTimeLayout stores timestamps as fixed-width millisecond RFC3339 UTC
// strings so lexical ordering matches chronological ordering in SQLite.
const sqliteTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// SQLiteStore implements Store backed by SQLite (modernc.org/sqlite, driver
// name "sqlite"). Steps and step runs are persisted as JSON columns. Call
// Migrate once before use to create the tables.
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewSQLiteStore wraps an *sql.DB. The caller owns the connection lifecycle.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db, now: time.Now}
}

// Migrate creates the workflow tables and indexes if they do not exist.
func (s *SQLiteStore) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite workflow store is not configured")
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS gateway_workflow_definitions (
	id TEXT PRIMARY KEY,
	tenant_id TEXT NOT NULL,
	app_id TEXT NOT NULL,
	name TEXT NOT NULL,
	steps_json TEXT NOT NULL,
	created_at TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_gateway_workflow_definitions_scope
	ON gateway_workflow_definitions (tenant_id, app_id, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS gateway_workflow_runs (
	id TEXT PRIMARY KEY,
	definition_id TEXT NOT NULL,
	tenant_id TEXT NOT NULL,
	app_id TEXT NOT NULL,
	state TEXT NOT NULL,
	current_step INTEGER NOT NULL,
	steps_json TEXT NOT NULL,
	idempotency_key TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_gateway_workflow_runs_scope
	ON gateway_workflow_runs (tenant_id, app_id, created_at, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_gateway_workflow_runs_idem
	ON gateway_workflow_runs (tenant_id, app_id, idempotency_key)
	WHERE idempotency_key <> ''`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) CreateDefinition(ctx context.Context, def Definition) (Definition, error) {
	if err := validateDefinition(def); err != nil {
		return Definition{}, err
	}
	if def.ID == "" {
		def.ID = newID("wfd")
	} else if existing, found, err := s.GetDefinition(ctx, def.TenantID, def.AppID, def.ID); err != nil {
		return Definition{}, err
	} else if found {
		return existing, nil
	}
	if def.CreatedAt.IsZero() {
		def.CreatedAt = s.now().UTC()
	} else {
		def.CreatedAt = def.CreatedAt.UTC()
	}
	stepsJSON, err := json.Marshal(cloneSteps(def.Steps))
	if err != nil {
		return Definition{}, err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO gateway_workflow_definitions (id, tenant_id, app_id, name, steps_json, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING`,
		def.ID, def.TenantID, def.AppID, def.Name, string(stepsJSON), formatSQLiteTime(def.CreatedAt))
	if err != nil {
		return Definition{}, err
	}
	stored, found, err := s.GetDefinition(ctx, def.TenantID, def.AppID, def.ID)
	if err != nil {
		return Definition{}, err
	}
	if !found {
		return Definition{}, ErrScope
	}
	return stored, nil
}

func (s *SQLiteStore) GetDefinition(ctx context.Context, tenantID string, appID string, id string) (Definition, bool, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return Definition{}, false, err
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, tenant_id, app_id, name, steps_json, created_at
FROM gateway_workflow_definitions
WHERE id = ? AND tenant_id = ? AND app_id = ?`, id, tenantID, appID)
	def, err := scanDefinition(row)
	if err == sql.ErrNoRows {
		return Definition{}, false, nil
	}
	if err != nil {
		return Definition{}, false, err
	}
	return def, true, nil
}

func (s *SQLiteStore) ListDefinitions(ctx context.Context, tenantID string, appID string, limit int) ([]Definition, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, tenant_id, app_id, name, steps_json, created_at
FROM gateway_workflow_definitions
WHERE tenant_id = ? AND app_id = ?
ORDER BY created_at ASC, id ASC
LIMIT ?`, tenantID, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Definition{}
	for rows.Next() {
		def, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, def)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) CreateRun(ctx context.Context, run Run) (Run, error) {
	if err := validateRun(run); err != nil {
		return Run{}, err
	}
	if run.IdempotencyKey != "" {
		if existing, found, err := s.runByIdempotency(ctx, run.TenantID, run.AppID, run.IdempotencyKey); err != nil {
			return Run{}, err
		} else if found {
			return existing, nil
		}
	}
	if run.ID == "" {
		run.ID = newID("wfr")
	}
	if run.State == "" {
		run.State = StatePending
	}
	now := s.now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	} else {
		run.CreatedAt = run.CreatedAt.UTC()
	}
	run.UpdatedAt = now
	stepsJSON, err := json.Marshal(cloneStepRuns(run.Steps))
	if err != nil {
		return Run{}, err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_workflow_runs (
	id, definition_id, tenant_id, app_id, state, current_step, steps_json, idempotency_key, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, app_id, idempotency_key) WHERE idempotency_key <> '' DO NOTHING`,
		run.ID, run.DefinitionID, run.TenantID, run.AppID, string(run.State), run.CurrentStep,
		string(stepsJSON), run.IdempotencyKey, formatSQLiteTime(run.CreatedAt), formatSQLiteTime(run.UpdatedAt))
	if err != nil {
		return Run{}, err
	}
	if inserted, _ := result.RowsAffected(); inserted == 0 && run.IdempotencyKey != "" {
		existing, found, err := s.runByIdempotency(ctx, run.TenantID, run.AppID, run.IdempotencyKey)
		if err != nil {
			return Run{}, err
		}
		if found {
			return existing, nil
		}
	}
	stored, found, err := s.GetRun(ctx, run.TenantID, run.AppID, run.ID)
	if err != nil {
		return Run{}, err
	}
	if !found {
		return Run{}, ErrScope
	}
	return stored, nil
}

func (s *SQLiteStore) GetRun(ctx context.Context, tenantID string, appID string, id string) (Run, bool, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return Run{}, false, err
	}
	row := s.db.QueryRowContext(ctx, selectRunSQL()+` WHERE id = ? AND tenant_id = ? AND app_id = ?`, id, tenantID, appID)
	run, err := scanRun(row)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

func (s *SQLiteStore) ListRuns(ctx context.Context, tenantID string, appID string, limit int) ([]Run, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	rows, err := s.db.QueryContext(ctx, selectRunSQL()+`
WHERE tenant_id = ? AND app_id = ?
ORDER BY created_at ASC, id ASC
LIMIT ?`, tenantID, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) UpdateRun(ctx context.Context, run Run) error {
	if err := validateRun(run); err != nil {
		return err
	}
	stepsJSON, err := json.Marshal(cloneStepRuns(run.Steps))
	if err != nil {
		return err
	}
	updatedAt := run.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = s.now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE gateway_workflow_runs
SET state = ?, current_step = ?, steps_json = ?, updated_at = ?
WHERE id = ? AND tenant_id = ? AND app_id = ?`,
		string(run.State), run.CurrentStep, string(stepsJSON), formatSQLiteTime(updatedAt.UTC()),
		run.ID, run.TenantID, run.AppID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) runByIdempotency(ctx context.Context, tenantID string, appID string, key string) (Run, bool, error) {
	row := s.db.QueryRowContext(ctx, selectRunSQL()+` WHERE tenant_id = ? AND app_id = ? AND idempotency_key = ?`, tenantID, appID, key)
	run, err := scanRun(row)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

func selectRunSQL() string {
	return `SELECT id, definition_id, tenant_id, app_id, state, current_step, steps_json, idempotency_key, created_at, updated_at FROM gateway_workflow_runs`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDefinition(row rowScanner) (Definition, error) {
	var (
		def       Definition
		stepsJSON string
		createdAt string
	)
	if err := row.Scan(&def.ID, &def.TenantID, &def.AppID, &def.Name, &stepsJSON, &createdAt); err != nil {
		return Definition{}, err
	}
	if err := json.Unmarshal([]byte(stepsJSON), &def.Steps); err != nil {
		return Definition{}, err
	}
	def.CreatedAt = parseSQLiteTime(createdAt)
	return def, nil
}

func scanRun(row rowScanner) (Run, error) {
	var (
		run       Run
		state     string
		stepsJSON string
		createdAt string
		updatedAt string
	)
	if err := row.Scan(&run.ID, &run.DefinitionID, &run.TenantID, &run.AppID, &state, &run.CurrentStep, &stepsJSON, &run.IdempotencyKey, &createdAt, &updatedAt); err != nil {
		return Run{}, err
	}
	run.State = RunState(state)
	if err := json.Unmarshal([]byte(stepsJSON), &run.Steps); err != nil {
		return Run{}, err
	}
	run.CreatedAt = parseSQLiteTime(createdAt)
	run.UpdatedAt = parseSQLiteTime(updatedAt)
	return run, nil
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
