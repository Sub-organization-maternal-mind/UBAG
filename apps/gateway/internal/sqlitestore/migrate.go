package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// migrateJobsScheduledSupport evolves pre-existing gateway_jobs tables to the
// scheduled-job shape (not_before column + widened status CHECK). Fresh
// databases already match via the embedded schema and skip this entirely.
//
// SQLite cannot ALTER a CHECK constraint, so drift is repaired with a
// transactional table rebuild that preserves every row: rename, recreate from
// the embedded schema, copy, drop. The CREATE TABLE and index DDL are
// extracted from the embedded schema at runtime — never a second copy — and
// the whole migration runs in one transaction, so a crash rolls back to the
// untouched old table and the next boot retries cleanly.
//
// The rebuild runs on one pinned connection with
// PRAGMA legacy_alter_table = ON and PRAGMA foreign_keys = OFF (both restored
// afterwards, even on failure):
//   - modern SQLite's ALTER TABLE RENAME rewrites the REFERENCES clauses of
//     every child table (gateway_job_events, gateway_job_worker_event_keys)
//     to point at gateway_jobs_migrate_backup; the DROP TABLE at the end of
//     the rebuild would then orphan those foreign keys. legacy_alter_table=ON
//     restores the pre-3.25 rename semantics: a pure rename that leaves
//     children pointing at the "gateway_jobs" name, which the recreated table
//     fills.
//   - foreign_keys=OFF is the documented SQLite table-rebuild procedure
//     (sqlite.org/lang_altertable.html, procedure 12): it keeps FK enforcement
//     from tripping over the intermediate rename/drop states.
//
// Pinning the connection matters: PRAGMA legacy_alter_table and foreign_keys
// are per-connection, so the pragma dance and the transaction must share one
// connection regardless of the pool size around db.
func migrateJobsScheduledSupport(ctx context.Context, db *sql.DB) error {
	tableSQL, err := tableDefinition(ctx, db, "gateway_jobs")
	if err != nil {
		return err
	}
	if strings.Contains(tableSQL, "not_before") && strings.Contains(tableSQL, "'scheduled'") {
		return nil
	}

	createStmt, indexStmts, err := extractJobsDDL()
	if err != nil {
		return err
	}
	oldColumns, err := tableColumns(ctx, db, "gateway_jobs")
	if err != nil {
		return err
	}
	columnList := strings.Join(oldColumns, ", ")

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlitestore: acquire connection for jobs migration: %w", err)
	}
	defer func() { _ = conn.Close() }()

	prevLegacyAlterTable := connPragma(ctx, conn, "legacy_alter_table")
	prevForeignKeys := connPragma(ctx, conn, "foreign_keys")
	if _, err := conn.ExecContext(ctx, `PRAGMA legacy_alter_table = ON`); err != nil {
		return fmt.Errorf("sqlitestore: set legacy_alter_table: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("sqlitestore: set foreign_keys off: %w", err)
	}
	defer func() {
		// Restore unconditionally (success, failure and panic) with a live
		// context: a cancelled migration context must not leak the pragmas
		// into the pooled connection.
		restoreCtx := context.Background()
		_, _ = conn.ExecContext(restoreCtx, `PRAGMA legacy_alter_table = `+strconv.Itoa(prevLegacyAlterTable))
		_, _ = conn.ExecContext(restoreCtx, `PRAGMA foreign_keys = `+strconv.Itoa(prevForeignKeys))
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlitestore: begin jobs migration: %w", err)
	}
	// A failed RENAME means a foreign gateway_jobs_migrate_backup table
	// exists; fail loud rather than touch data that is not ours.
	if _, err := tx.ExecContext(ctx, `ALTER TABLE gateway_jobs RENAME TO gateway_jobs_migrate_backup`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlitestore: rename gateway_jobs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, createStmt); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlitestore: recreate gateway_jobs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO gateway_jobs (%s) SELECT %s FROM gateway_jobs_migrate_backup", columnList, columnList,
	)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlitestore: copy gateway_jobs rows: %w", err)
	}
	for _, indexStmt := range indexStmts {
		if _, err := tx.ExecContext(ctx, indexStmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlitestore: recreate gateway_jobs index: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE gateway_jobs_migrate_backup`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlitestore: drop gateway_jobs backup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlitestore: commit gateway_jobs migration: %w", err)
	}
	return nil
}

// connPragma reads an integer PRAGMA value from a specific connection.
// Errors are swallowed and reported as 0: the value is only used to restore
// the previous state, and a missing pragma restores to OFF, which is the
// SQLite default for both pragmas used here.
func connPragma(ctx context.Context, conn *sql.Conn, name string) int {
	var value int
	_ = conn.QueryRowContext(ctx, "PRAGMA "+name).Scan(&value)
	return value
}

func tableDefinition(ctx context.Context, db *sql.DB, table string) (string, error) {
	var definition sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&definition)
	if err != nil {
		return "", err
	}
	return definition.String, nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, `"`+name+`"`)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

// extractJobsDDL pulls the gateway_jobs CREATE TABLE statement and its
// idx_gateway_jobs_* index statements out of the embedded schema. Strict:
// any format drift fails closed with an error (caught in CI) rather than
// migrating with a half-parsed definition.
func extractJobsDDL() (create string, indexes []string, err error) {
	create, err = extractCreateTable(schemaSQL, "gateway_jobs")
	if err != nil {
		return "", nil, err
	}
	for _, stmt := range splitStatements(schemaSQL) {
		trimmed := strings.TrimSpace(stmt)
		if strings.HasPrefix(strings.ToUpper(trimmed), "CREATE INDEX") &&
			strings.Contains(strings.ToUpper(trimmed), "IDX_GATEWAY_JOBS") {
			if !strings.HasSuffix(trimmed, ";") {
				trimmed += ";"
			}
			indexes = append(indexes, trimmed)
		}
	}
	if len(indexes) == 0 {
		return "", nil, fmt.Errorf("sqlitestore: no idx_gateway_jobs indexes found in embedded schema")
	}
	return create, indexes, nil
}

func extractCreateTable(schema, table string) (string, error) {
	marker := "CREATE TABLE IF NOT EXISTS " + table + " ("
	start := strings.Index(schema, marker)
	if start < 0 {
		return "", fmt.Errorf("sqlitestore: %s definition not found in embedded schema", table)
	}
	depth := 0
	for i := start; i < len(schema); i++ {
		switch schema[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				rest := schema[i+1:]
				semi := strings.Index(rest, ";")
				if semi < 0 {
					return "", fmt.Errorf("sqlitestore: %s definition unterminated", table)
				}
				return strings.TrimSpace(schema[start:i+1]) + ";", nil
			}
		}
	}
	return "", fmt.Errorf("sqlitestore: %s definition unbalanced", table)
}

func splitStatements(schema string) []string {
	var statements []string
	depth := 0
	current := strings.Builder{}
	for _, line := range strings.Split(schema, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		for _, ch := range line {
			switch ch {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		current.WriteString(line)
		current.WriteString("\n")
		if depth == 0 && strings.Contains(line, ";") {
			statements = append(statements, current.String())
			current.Reset()
		}
	}
	return statements
}

// migrateIdempotencyLockColumns evolves pre-existing gateway_idempotency_records
// tables to the in-flight lock shape (status + locked_until columns). Fresh
// databases already match via the embedded schema and skip this entirely.
//
// Unlike the jobs migration this is a plain additive change, so ALTER TABLE ...
// ADD COLUMN is sufficient and preserves every row: legacy rows keep NULL in
// both columns, which the idempotency stores read as the legacy
// reserve-until-TTL behavior (never as an in-flight lock).
func migrateIdempotencyLockColumns(ctx context.Context, db *sql.DB) error {
	columns, err := tableColumns(ctx, db, "gateway_idempotency_records")
	if err != nil {
		return fmt.Errorf("sqlitestore: inspect gateway_idempotency_records: %w", err)
	}
	if len(columns) == 0 {
		// Table absent: the embedded schema above created every current table,
		// so a missing table means the database is not a gateway database.
		return nil
	}
	present := make(map[string]bool, len(columns))
	for _, column := range columns {
		present[strings.Trim(column, `"`)] = true
	}
	additions := map[string]string{
		"status":       `ALTER TABLE gateway_idempotency_records ADD COLUMN status TEXT`,
		"locked_until": `ALTER TABLE gateway_idempotency_records ADD COLUMN locked_until TEXT`,
	}
	// Deterministic order regardless of map iteration.
	for _, column := range []string{"status", "locked_until"} {
		if present[column] {
			continue
		}
		if _, err := db.ExecContext(ctx, additions[column]); err != nil {
			return fmt.Errorf("sqlitestore: add gateway_idempotency_records.%s: %w", column, err)
		}
	}
	return nil
}
