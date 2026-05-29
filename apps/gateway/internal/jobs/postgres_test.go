package jobs

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresStoreContract(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}

	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresGatewayMigration(t, db)

	store := NewPostgresStore(db)
	tenantID := "tenant_pg_jobs_" + time.Now().UTC().Format("20060102150405")
	appID := "app_pg_jobs"
	defer cleanupPostgresJobs(t, db, tenantID)

	job, err := store.Create(context.Background(), CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       tenantID,
		AppID:          appID,
		IdempotencyKey: "idem_pg_jobs_contract",
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        "trace_pg_jobs_contract",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if job.Status != StatusQueued {
		t.Fatalf("status = %s, want %s", job.Status, StatusQueued)
	}
	if _, found, err := store.GetScoped(context.Background(), job.ID, tenantID, appID); err != nil || !found {
		t.Fatalf("GetScoped own scope found=%v err=%v", found, err)
	}
	if _, found, err := store.GetScoped(context.Background(), job.ID, tenantID+"_other", appID); err != nil || found {
		t.Fatalf("GetScoped other tenant found=%v err=%v, want hidden", found, err)
	}

	events, found, err := store.ListEvents(context.Background(), job.ID, 0, 10)
	if err != nil || !found {
		t.Fatalf("ListEvents found=%v err=%v", found, err)
	}
	if len(events) != 1 || events[0].Type != "queued" || events[0].Sequence != 1 {
		t.Fatalf("queued event mismatch: %#v", events)
	}

	running := WorkerEvent{EventID: "pg_worker_evt_running", JobID: job.ID, APIVersion: job.APIVersion, Type: "running", Sequence: 2, TraceID: job.TraceID, Data: map[string]any{"status": "running"}}
	if _, found, err := store.ApplyWorkerEvent(context.Background(), running); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent running found=%v err=%v", found, err)
	}
	if _, found, err := store.ApplyWorkerEvent(context.Background(), running); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent duplicate found=%v err=%v", found, err)
	}
	completed, found, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{
		EventID:    "pg_worker_evt_completed",
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       "completed",
		Sequence:   3,
		TraceID:    job.TraceID,
		Data:       map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "done"}},
	})
	if err != nil || !found {
		t.Fatalf("ApplyWorkerEvent completed found=%v err=%v", found, err)
	}
	if completed.Status != StatusCompleted || completed.Result == nil {
		t.Fatalf("completed job mismatch: %#v", completed)
	}

	events, found, err = store.ListEvents(context.Background(), job.ID, 0, 10)
	if err != nil || !found {
		t.Fatalf("ListEvents after worker found=%v err=%v", found, err)
	}
	if len(events) != 3 {
		t.Fatalf("event count = %d, want queued plus two worker events", len(events))
	}

	counts, total, err := store.CountsByStatus(context.Background(), ListFilter{TenantID: tenantID, AppID: appID})
	if err != nil {
		t.Fatalf("CountsByStatus returned error: %v", err)
	}
	if total != 1 || counts[StatusCompleted] != 1 {
		t.Fatalf("counts = %#v total=%d, want one completed job", counts, total)
	}
	listed, err := store.List(context.Background(), ListFilter{TenantID: tenantID, AppID: appID, Status: string(StatusCompleted), Target: "mock"})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != job.ID {
		t.Fatalf("filtered list = %#v, want completed job", listed)
	}
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("Ready returned error: %v", err)
	}
}

func TestPostgresStoreRedactsUnsafeWorkerEventData(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}

	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresGatewayMigration(t, db)

	store := NewPostgresStore(db)
	tenantID := "tenant_pg_redact_" + time.Now().UTC().Format("20060102150405")
	defer cleanupPostgresJobs(t, db, tenantID)
	job, err := store.Create(context.Background(), CreateRequest{
		APIVersion:     "2026-05-22",
		TenantID:       tenantID,
		AppID:          "app_pg_redact",
		IdempotencyKey: "idem_pg_redact",
		Target:         "mock",
		CommandType:    "submit",
		Input:          map[string]any{"prompt": "hello"},
		TraceID:        "trace_pg_redact",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	_, found, err := store.ApplyWorkerEvent(context.Background(), WorkerEvent{
		EventID:    "pg_manual_session",
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       "session.manual_action_required",
		Sequence:   2,
		TraceID:    job.TraceID,
		Data:       map[string]any{"status": "running", "novnc_url": "http://127.0.0.1:7900/session/sess_1", "session_id": "sess_1"},
	})
	if err != nil || !found {
		t.Fatalf("ApplyWorkerEvent found=%v err=%v", found, err)
	}

	events, found, err := store.ListEvents(context.Background(), job.ID, 0, 10)
	if err != nil || !found {
		t.Fatalf("ListEvents found=%v err=%v", found, err)
	}
	data := events[1].Data
	if data["novnc_url"] != "[redacted]" || data["session_id"] != "[redacted]" {
		t.Fatalf("unsafe data was not redacted: %#v", data)
	}
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

func applyPostgresGatewayMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "migrations", "postgres", "0001_gateway_stores.sql")
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), string(sqlBytes)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
}

func cleanupPostgresJobs(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM gateway_jobs WHERE tenant_id = $1`, tenantID); err != nil {
		t.Fatalf("cleanup jobs: %v", err)
	}
}
