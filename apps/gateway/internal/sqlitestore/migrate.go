package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
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

	tx, err := db.BeginTx(ctx, nil)
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
