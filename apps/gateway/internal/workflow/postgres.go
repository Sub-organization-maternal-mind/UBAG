package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

var _ Store = (*PostgresStore)(nil)

// PostgresStore implements Store backed by Postgres (github.com/jackc/pgx/v5/stdlib,
// driver name "pgx"). Steps and step runs are persisted as JSONB columns. The
// schema is migration-driven (migrations/postgres/0005_enterprise_stores.sql);
// call Ready to verify the required tables exist before serving traffic.
type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewPostgresStore wraps an *sql.DB. The caller owns the connection lifecycle.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, now: time.Now}
}

// Ready pings the database and verifies the workflow tables exist.
func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("postgres workflow store is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return err
	}
	for _, objectName := range []string{"gateway_workflow_definitions", "gateway_workflow_runs"} {
		if err := requirePostgresObject(ctx, p.db, objectName); err != nil {
			return err
		}
	}
	return nil
}

func (p *PostgresStore) CreateDefinition(ctx context.Context, def Definition) (Definition, error) {
	if err := validateDefinition(def); err != nil {
		return Definition{}, err
	}
	if def.ID == "" {
		def.ID = newID("wfd")
	} else if existing, found, err := p.GetDefinition(ctx, def.TenantID, def.AppID, def.ID); err != nil {
		return Definition{}, err
	} else if found {
		return existing, nil
	}
	if def.CreatedAt.IsZero() {
		def.CreatedAt = p.now().UTC()
	} else {
		def.CreatedAt = def.CreatedAt.UTC()
	}
	stepsJSON, err := json.Marshal(cloneSteps(def.Steps))
	if err != nil {
		return Definition{}, err
	}
	_, err = p.db.ExecContext(ctx, `
INSERT INTO gateway_workflow_definitions (id, tenant_id, app_id, name, steps_json, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO NOTHING`,
		def.ID, def.TenantID, def.AppID, def.Name, json.RawMessage(stepsJSON), def.CreatedAt)
	if err != nil {
		return Definition{}, fmt.Errorf("workflow create definition: %w", err)
	}
	stored, found, err := p.GetDefinition(ctx, def.TenantID, def.AppID, def.ID)
	if err != nil {
		return Definition{}, err
	}
	if !found {
		return Definition{}, ErrScope
	}
	return stored, nil
}

func (p *PostgresStore) GetDefinition(ctx context.Context, tenantID string, appID string, id string) (Definition, bool, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return Definition{}, false, err
	}
	row := p.db.QueryRowContext(ctx, `
SELECT id, tenant_id, app_id, name, steps_json, created_at
FROM gateway_workflow_definitions
WHERE id = $1 AND tenant_id = $2 AND app_id = $3`, id, tenantID, appID)
	def, err := scanPostgresDefinition(row)
	if err == sql.ErrNoRows {
		return Definition{}, false, nil
	}
	if err != nil {
		return Definition{}, false, err
	}
	return def, true, nil
}

func (p *PostgresStore) ListDefinitions(ctx context.Context, tenantID string, appID string, limit int) ([]Definition, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	rows, err := p.db.QueryContext(ctx, `
SELECT id, tenant_id, app_id, name, steps_json, created_at
FROM gateway_workflow_definitions
WHERE tenant_id = $1 AND app_id = $2
ORDER BY created_at ASC, id ASC
LIMIT $3`, tenantID, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Definition{}
	for rows.Next() {
		def, err := scanPostgresDefinition(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, def)
	}
	return result, rows.Err()
}

func (p *PostgresStore) CreateRun(ctx context.Context, run Run) (Run, error) {
	if err := validateRun(run); err != nil {
		return Run{}, err
	}
	if run.IdempotencyKey != "" {
		if existing, found, err := p.runByIdempotency(ctx, run.TenantID, run.AppID, run.IdempotencyKey); err != nil {
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
	now := p.now().UTC()
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
	result, err := p.db.ExecContext(ctx, `
INSERT INTO gateway_workflow_runs (
	id, definition_id, tenant_id, app_id, state, current_step, steps_json, idempotency_key, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (tenant_id, app_id, idempotency_key) WHERE idempotency_key <> '' DO NOTHING`,
		run.ID, run.DefinitionID, run.TenantID, run.AppID, string(run.State), run.CurrentStep,
		json.RawMessage(stepsJSON), run.IdempotencyKey, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return Run{}, fmt.Errorf("workflow create run: %w", err)
	}
	if inserted, _ := result.RowsAffected(); inserted == 0 && run.IdempotencyKey != "" {
		existing, found, err := p.runByIdempotency(ctx, run.TenantID, run.AppID, run.IdempotencyKey)
		if err != nil {
			return Run{}, err
		}
		if found {
			return existing, nil
		}
	}
	stored, found, err := p.GetRun(ctx, run.TenantID, run.AppID, run.ID)
	if err != nil {
		return Run{}, err
	}
	if !found {
		return Run{}, ErrScope
	}
	return stored, nil
}

func (p *PostgresStore) GetRun(ctx context.Context, tenantID string, appID string, id string) (Run, bool, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return Run{}, false, err
	}
	row := p.db.QueryRowContext(ctx, selectPostgresRunSQL()+` WHERE id = $1 AND tenant_id = $2 AND app_id = $3`, id, tenantID, appID)
	run, err := scanPostgresRun(row)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

func (p *PostgresStore) ListRuns(ctx context.Context, tenantID string, appID string, limit int) ([]Run, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	rows, err := p.db.QueryContext(ctx, selectPostgresRunSQL()+`
WHERE tenant_id = $1 AND app_id = $2
ORDER BY created_at ASC, id ASC
LIMIT $3`, tenantID, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Run{}
	for rows.Next() {
		run, err := scanPostgresRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func (p *PostgresStore) UpdateRun(ctx context.Context, run Run) error {
	if err := validateRun(run); err != nil {
		return err
	}
	stepsJSON, err := json.Marshal(cloneStepRuns(run.Steps))
	if err != nil {
		return err
	}
	updatedAt := run.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = p.now().UTC()
	}
	result, err := p.db.ExecContext(ctx, `
UPDATE gateway_workflow_runs
SET state = $1, current_step = $2, steps_json = $3, updated_at = $4
WHERE id = $5 AND tenant_id = $6 AND app_id = $7`,
		string(run.State), run.CurrentStep, json.RawMessage(stepsJSON), updatedAt.UTC(),
		run.ID, run.TenantID, run.AppID)
	if err != nil {
		return fmt.Errorf("workflow update run: %w", err)
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

func (p *PostgresStore) runByIdempotency(ctx context.Context, tenantID string, appID string, key string) (Run, bool, error) {
	row := p.db.QueryRowContext(ctx, selectPostgresRunSQL()+` WHERE tenant_id = $1 AND app_id = $2 AND idempotency_key = $3`, tenantID, appID, key)
	run, err := scanPostgresRun(row)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

func selectPostgresRunSQL() string {
	return `SELECT id, definition_id, tenant_id, app_id, state, current_step, steps_json, idempotency_key, created_at, updated_at FROM gateway_workflow_runs`
}

func scanPostgresDefinition(row rowScanner) (Definition, error) {
	var (
		def       Definition
		stepsJSON []byte
	)
	if err := row.Scan(&def.ID, &def.TenantID, &def.AppID, &def.Name, &stepsJSON, &def.CreatedAt); err != nil {
		return Definition{}, err
	}
	if err := json.Unmarshal(stepsJSON, &def.Steps); err != nil {
		return Definition{}, err
	}
	def.CreatedAt = def.CreatedAt.UTC()
	return def, nil
}

func scanPostgresRun(row rowScanner) (Run, error) {
	var (
		run       Run
		state     string
		stepsJSON []byte
	)
	if err := row.Scan(&run.ID, &run.DefinitionID, &run.TenantID, &run.AppID, &state, &run.CurrentStep, &stepsJSON, &run.IdempotencyKey, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return Run{}, err
	}
	run.State = RunState(state)
	if err := json.Unmarshal(stepsJSON, &run.Steps); err != nil {
		return Run{}, err
	}
	run.CreatedAt = run.CreatedAt.UTC()
	run.UpdatedAt = run.UpdatedAt.UTC()
	return run, nil
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
