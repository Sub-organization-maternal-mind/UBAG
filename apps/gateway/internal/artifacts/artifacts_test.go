package artifacts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
)

// TestMemoryArtifactStore verifies the in-process implementation.
func TestMemoryArtifactStore(t *testing.T) {
	ctx := context.Background()
	s := artifacts.NewMemoryArtifactStore()

	t.Run("Ready", func(t *testing.T) {
		if err := s.Ready(ctx); err != nil {
			t.Fatalf("Ready: %v", err)
		}
	})

	t.Run("PutAndGet", func(t *testing.T) {
		data := []byte("hello artifact")
		rec, err := s.PutArtifact(ctx, "job-001", "screenshot.png", "image/png", bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("PutArtifact: %v", err)
		}
		if rec.JobID != "job-001" {
			t.Errorf("rec.JobID = %q; want job-001", rec.JobID)
		}
		if rec.Key != "screenshot.png" {
			t.Errorf("rec.Key = %q; want screenshot.png", rec.Key)
		}
		if rec.SizeBytes != int64(len(data)) {
			t.Errorf("rec.SizeBytes = %d; want %d", rec.SizeBytes, len(data))
		}
		if rec.Checksum == "" {
			t.Error("rec.Checksum is empty")
		}
		sum := sha256.Sum256(data)
		if rec.Checksum != hex.EncodeToString(sum[:]) {
			t.Errorf("checksum = %q; want SHA-256", rec.Checksum)
		}

		rc, got, err := s.GetArtifact(ctx, "job-001", "screenshot.png")
		if err != nil {
			t.Fatalf("GetArtifact: %v", err)
		}
		defer rc.Close()
		if got.Key != "screenshot.png" {
			t.Errorf("got.Key = %q; want screenshot.png", got.Key)
		}
		gotData, _ := io.ReadAll(rc)
		if !bytes.Equal(gotData, data) {
			t.Errorf("data mismatch: got %q; want %q", gotData, data)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		_, _, err := s.GetArtifact(ctx, "job-missing", "file.txt")
		if !artifacts.IsNotFound(err) {
			t.Errorf("expected ErrArtifactNotFound, got %v", err)
		}
	})

	t.Run("List", func(t *testing.T) {
		data := []byte("x")
		for _, key := range []string{"a.txt", "b.txt", "c.txt"} {
			if _, err := s.PutArtifact(ctx, "job-list", key, "text/plain", bytes.NewReader(data), int64(len(data))); err != nil {
				t.Fatalf("PutArtifact %s: %v", key, err)
			}
		}
		recs, err := s.ListArtifacts(ctx, "job-list")
		if err != nil {
			t.Fatalf("ListArtifacts: %v", err)
		}
		if len(recs) != 3 {
			t.Errorf("ListArtifacts returned %d records; want 3", len(recs))
		}
	})

	t.Run("CompositeKeyDoesNotCollide", func(t *testing.T) {
		if _, err := s.PutArtifact(ctx, "job/a", "b", "text/plain", strings.NewReader("first"), int64(len("first"))); err != nil {
			t.Fatalf("PutArtifact first: %v", err)
		}
		if _, err := s.PutArtifact(ctx, "job", "a/b", "text/plain", strings.NewReader("second"), int64(len("second"))); err != nil {
			t.Fatalf("PutArtifact second: %v", err)
		}
		rc, _, err := s.GetArtifact(ctx, "job/a", "b")
		if err != nil {
			t.Fatalf("GetArtifact first: %v", err)
		}
		defer rc.Close()
		got, _ := io.ReadAll(rc)
		if string(got) != "first" {
			t.Fatalf("collided artifact data = %q", got)
		}
	})

	t.Run("InvalidIdentifiers", func(t *testing.T) {
		if _, err := s.PutArtifact(ctx, "", "key", "text/plain", strings.NewReader("x"), 1); !artifacts.IsInvalid(err) {
			t.Fatalf("empty job error = %v, want invalid", err)
		}
		if _, err := s.PutArtifact(ctx, "job", "", "text/plain", strings.NewReader("x"), 1); !artifacts.IsInvalid(err) {
			t.Fatalf("empty key error = %v, want invalid", err)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		data := []byte("to delete")
		if _, err := s.PutArtifact(ctx, "job-del", "del.txt", "text/plain", bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("PutArtifact: %v", err)
		}
		if err := s.DeleteArtifact(ctx, "job-del", "del.txt"); err != nil {
			t.Fatalf("DeleteArtifact: %v", err)
		}
		_, _, err := s.GetArtifact(ctx, "job-del", "del.txt")
		if !artifacts.IsNotFound(err) {
			t.Errorf("expected not-found after delete, got %v", err)
		}
	})
}

func TestMemoryArtifactMeta(t *testing.T) {
	ctx := context.Background()
	meta := artifacts.NewMemoryArtifactMeta()
	if err := meta.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	rec := artifacts.ArtifactRecord{
		JobID:       "meta-job",
		Key:         "report.txt",
		Bucket:      "memory",
		ObjectKey:   "objects/meta-job/report.txt",
		ContentType: "text/plain",
		SizeBytes:   6,
		Checksum:    "abc",
		CreatedAt:   time.Now().UTC(),
	}
	if err := meta.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := meta.Get(ctx, rec.JobID, rec.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ObjectKey != rec.ObjectKey || got.Checksum != rec.Checksum {
		t.Fatalf("metadata mismatch: %#v", got)
	}
	list, err := meta.List(ctx, rec.JobID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Key != rec.Key {
		t.Fatalf("list mismatch: %#v", list)
	}
	if err := meta.Delete(ctx, rec.JobID, rec.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := meta.Get(ctx, rec.JobID, rec.Key); !artifacts.IsNotFound(err) {
		t.Fatalf("Get after delete error = %v, want not found", err)
	}
}

func TestMinIOArtifactStoreConstructorValidation(t *testing.T) {
	if _, err := artifacts.NewMinIOArtifactStore("", "access", "secret", "bucket", false, nil); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("endpoint error = %v", err)
	}
	if _, err := artifacts.NewMinIOArtifactStore("localhost:9000", "", "secret", "bucket", false, nil); err == nil || !strings.Contains(err.Error(), "access key") {
		t.Fatalf("credential error = %v", err)
	}
	if store, err := artifacts.NewMinIOArtifactStore("localhost:9000", "access", "secret", "", false, nil); err != nil || store == nil {
		t.Fatalf("default bucket constructor returned store=%v err=%v", store, err)
	}
}

func TestPostgresArtifactMetaContract(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}

	ctx := context.Background()
	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresMigration(t, db, "0001_gateway_stores.sql")
	applyPostgresMigration(t, db, "0002_artifact_metadata.sql")
	if err := artifacts.VerifyArtifactMetaSchema(ctx, db); err != nil {
		t.Fatalf("VerifyArtifactMetaSchema after 0002: %v", err)
	}

	meta := artifacts.NewPostgresArtifactMeta(db)
	jobID := "artifact_pg_" + time.Now().UTC().Format("20060102150405")
	defer func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM artifact_metadata WHERE job_id = $1`, jobID)
	}()
	rec := artifacts.ArtifactRecord{
		JobID:       jobID,
		Key:         "result.txt",
		Bucket:      "ubag-artifacts-test",
		ObjectKey:   "objects/result-v1",
		ContentType: "text/plain",
		SizeBytes:   5,
		Checksum:    "sum1",
		CreatedAt:   time.Now().UTC(),
	}
	if err := meta.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rec.ObjectKey = "objects/result-v2"
	rec.Checksum = "sum2"
	if err := meta.Put(ctx, rec); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}
	got, err := meta.Get(ctx, jobID, "result.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ObjectKey != "objects/result-v2" || got.Checksum != "sum2" {
		t.Fatalf("updated metadata mismatch: %#v", got)
	}
	list, err := meta.List(ctx, jobID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Key != "result.txt" {
		t.Fatalf("list mismatch: %#v", list)
	}
	if err := meta.Delete(ctx, jobID, "result.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := meta.Get(ctx, jobID, "result.txt"); !artifacts.IsNotFound(err) {
		t.Fatalf("Get after delete error = %v, want not found", err)
	}
}

// TestMinIOArtifactStore runs when UBAG_TEST_MINIO_ENDPOINT is set.
func TestMinIOArtifactStore(t *testing.T) {
	endpoint := os.Getenv("UBAG_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("UBAG_TEST_MINIO_ENDPOINT not set; skipping MinIO integration tests")
	}
	accessKey := os.Getenv("UBAG_TEST_MINIO_ACCESS_KEY")
	if accessKey == "" {
		accessKey = "minioadmin"
	}
	secretKey := os.Getenv("UBAG_TEST_MINIO_SECRET_KEY")
	if secretKey == "" {
		secretKey = "minioadmin"
	}

	ctx := context.Background()
	s, err := artifacts.NewMinIOArtifactStore(endpoint, accessKey, secretKey, "ubag-artifacts-test", false, nil)
	if err != nil {
		t.Fatalf("NewMinIOArtifactStore: %v", err)
	}

	t.Run("Ready", func(t *testing.T) {
		if err := s.Ready(ctx); err != nil {
			t.Fatalf("Ready: %v", err)
		}
	})

	t.Run("PutAndGet", func(t *testing.T) {
		content := "minio integration test content"
		data := strings.NewReader(content)
		rec, err := s.PutArtifact(ctx, "minio-job-001", "report.txt", "text/plain", data, int64(len(content)))
		if err != nil {
			t.Fatalf("PutArtifact: %v", err)
		}
		if rec.JobID != "minio-job-001" {
			t.Errorf("rec.JobID = %q; want minio-job-001", rec.JobID)
		}

		rc, got, err := s.GetArtifact(ctx, "minio-job-001", "report.txt")
		if err != nil {
			t.Fatalf("GetArtifact: %v", err)
		}
		defer rc.Close()
		if got.Bucket != "ubag-artifacts-test" {
			t.Errorf("got.Bucket = %q; want ubag-artifacts-test", got.Bucket)
		}
		gotData, _ := io.ReadAll(rc)
		if string(gotData) != content {
			t.Errorf("data mismatch: got %q; want %q", gotData, content)
		}
	})

	t.Run("List", func(t *testing.T) {
		for _, key := range []string{"img1.png", "img2.png"} {
			d := []byte("img")
			if _, err := s.PutArtifact(ctx, "minio-job-list", key, "image/png", bytes.NewReader(d), int64(len(d))); err != nil {
				t.Fatalf("PutArtifact %s: %v", key, err)
			}
		}
		recs, err := s.ListArtifacts(ctx, "minio-job-list")
		if err != nil {
			t.Fatalf("ListArtifacts: %v", err)
		}
		if len(recs) < 2 {
			t.Errorf("ListArtifacts returned %d records; want >= 2", len(recs))
		}
	})
}

func openPostgresTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("PingContext returned error: %v", err)
	}
	return db
}

func applyPostgresMigration(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "migrations", "postgres", name)
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	if _, err := db.ExecContext(context.Background(), string(sqlBytes)); err != nil {
		t.Fatalf("apply migration %s: %v", name, err)
	}
}
