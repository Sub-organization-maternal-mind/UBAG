package artifacts

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PostgresArtifactMeta implements ArtifactMeta using the artifact_metadata
// Postgres table created by migrations/postgres/0002_artifact_metadata.sql.
type PostgresArtifactMeta struct {
	db *sql.DB
}

func NewPostgresArtifactMeta(db *sql.DB) *PostgresArtifactMeta {
	return &PostgresArtifactMeta{db: db}
}

func (p *PostgresArtifactMeta) Ready(ctx context.Context) error {
	return VerifyArtifactMetaSchema(ctx, p.db)
}

func (p *PostgresArtifactMeta) Put(ctx context.Context, rec ArtifactRecord) error {
	if _, err := makeArtifactMapKey(rec.JobID, rec.Key); err != nil {
		return err
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO artifact_metadata
			(job_id, artifact_key, bucket, object_key, content_type, size_bytes, checksum, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (job_id, artifact_key) DO UPDATE SET
			bucket       = EXCLUDED.bucket,
			object_key   = EXCLUDED.object_key,
			content_type = EXCLUDED.content_type,
			size_bytes   = EXCLUDED.size_bytes,
			checksum     = EXCLUDED.checksum,
			created_at   = EXCLUDED.created_at`,
		rec.JobID,
		rec.Key,
		rec.Bucket,
		rec.ObjectKey,
		rec.ContentType,
		rec.SizeBytes,
		rec.Checksum,
		rec.CreatedAt.UTC(),
	)
	return err
}

func (p *PostgresArtifactMeta) Get(ctx context.Context, jobID, key string) (ArtifactRecord, error) {
	if _, err := makeArtifactMapKey(jobID, key); err != nil {
		return ArtifactRecord{}, err
	}
	row := p.db.QueryRowContext(ctx, `
		SELECT job_id, artifact_key, bucket, object_key, content_type, size_bytes, checksum, created_at
		FROM artifact_metadata
		WHERE job_id = $1 AND artifact_key = $2`, jobID, key)

	var rec ArtifactRecord
	var checksum sql.NullString
	var objectKey sql.NullString
	if err := row.Scan(&rec.JobID, &rec.Key, &rec.Bucket, &objectKey, &rec.ContentType, &rec.SizeBytes, &checksum, &rec.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return ArtifactRecord{}, &ErrArtifactNotFound{JobID: jobID, Key: key}
		}
		return ArtifactRecord{}, fmt.Errorf("artifact_metadata get: %w", err)
	}
	rec.ObjectKey = objectKey.String
	rec.Checksum = checksum.String
	return rec, nil
}

func (p *PostgresArtifactMeta) List(ctx context.Context, jobID string) ([]ArtifactRecord, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, &ErrArtifactInvalid{Field: "job_id"}
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT job_id, artifact_key, bucket, object_key, content_type, size_bytes, checksum, created_at
		FROM artifact_metadata
		WHERE job_id = $1
		ORDER BY created_at DESC, artifact_key ASC`, jobID)
	if err != nil {
		return nil, fmt.Errorf("artifact_metadata list: %w", err)
	}
	defer rows.Close()

	var result []ArtifactRecord
	for rows.Next() {
		var rec ArtifactRecord
		var checksum sql.NullString
		var objectKey sql.NullString
		if err := rows.Scan(&rec.JobID, &rec.Key, &rec.Bucket, &objectKey, &rec.ContentType, &rec.SizeBytes, &checksum, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("artifact_metadata list scan: %w", err)
		}
		rec.ObjectKey = objectKey.String
		rec.Checksum = checksum.String
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artifact_metadata list rows: %w", err)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Key < result[j].Key
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (p *PostgresArtifactMeta) Delete(ctx context.Context, jobID, key string) error {
	if _, err := makeArtifactMapKey(jobID, key); err != nil {
		return err
	}
	result, err := p.db.ExecContext(ctx,
		`DELETE FROM artifact_metadata WHERE job_id = $1 AND artifact_key = $2`,
		jobID, key)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return &ErrArtifactNotFound{JobID: jobID, Key: key}
	}
	return err
}

// VerifyArtifactMetaSchema checks that the artifact_metadata table exists and
// has the expected columns.  Used by /v1/ready when Postgres store is enabled.
func VerifyArtifactMetaSchema(ctx context.Context, db *sql.DB) error {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_name = 'artifact_metadata'
		)`).Scan(&exists)
	if err != nil {
		return fmt.Errorf("artifact_metadata table check failed: %w", err)
	}
	if !exists {
		return fmt.Errorf("artifact_metadata table is missing; apply migrations/postgres/0002_artifact_metadata.sql")
	}

	requiredColumns := []string{"job_id", "artifact_key", "bucket", "object_key", "content_type", "size_bytes", "checksum", "created_at"}
	for _, column := range requiredColumns {
		var colExists bool
		err = db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				AND table_name = 'artifact_metadata'
				AND column_name = $1
			)`, column).Scan(&colExists)
		if err != nil {
			return fmt.Errorf("artifact_metadata column check failed: %w", err)
		}
		if !colExists {
			return fmt.Errorf("artifact_metadata.%s column is missing; apply migrations/postgres/0002_artifact_metadata.sql", column)
		}
	}
	return nil
}

// readArtifactRow scans a single artifact_metadata row (helper).
func readArtifactRow(rows *sql.Rows) (ArtifactRecord, error) {
	var rec ArtifactRecord
	var checksum sql.NullString
	var objectKey sql.NullString
	var createdAt time.Time
	if err := rows.Scan(&rec.JobID, &rec.Key, &rec.Bucket, &objectKey, &rec.ContentType, &rec.SizeBytes, &checksum, &createdAt); err != nil {
		return ArtifactRecord{}, err
	}
	rec.ObjectKey = objectKey.String
	rec.Checksum = checksum.String
	rec.CreatedAt = createdAt
	return rec, nil
}
