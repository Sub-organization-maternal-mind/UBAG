package httpapi

import (
	"context"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// TestReservationFailMarksJobAndReleasesScope exercises the createJob failure
// paths through the reservation: the job lands in failed_retryable, the
// idempotency scope is released (the key is reservable again), and the
// concurrency token is freed.
func TestReservationFailMarksJobAndReleasesScope(t *testing.T) {
	ctx := context.Background()
	srv := NewServer(Config{
		Version:     "test",
		AppSecret:   "dev-secret",
		ActorRole:   "service",
		TenantID:    "tenant_root",
		AppID:       "app_root",
		Concurrency: topology.NewConcurrencyRegistry(),
	})
	job, err := srv.jobs.Create(ctx, jobstore.CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    "t",
		AppID:       "a",
		Target:      "mock",
		CommandType: "submit",
		Input:       map[string]any{},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	scope := idempotency.Scope{TenantID: "t", AppID: "a", Operation: "create_job", Key: "test-key-1"}
	res := srv.newJobReservation(scope, "t", "mock", "a")
	srv.markConcurrencyAcquired(job.ID, "t", "mock", "a")
	res.attachJob(job.ID)
	res.fail(ctx)

	got, found, err := srv.jobs.Get(ctx, job.ID)
	if err != nil || !found {
		t.Fatalf("get job: found=%v err=%v", found, err)
	}
	if got.Status != jobstore.StatusFailedRetryable {
		t.Fatalf("job status = %q; want failed_retryable", got.Status)
	}

	dec, err := srv.idempotency.Reserve(ctx, scope, "hash-abc")
	if err != nil {
		t.Fatalf("re-reserve scope: %v", err)
	}
	if dec.Kind == idempotency.DecisionConflict {
		t.Fatal("idempotency scope still held after fail(); expected release")
	}

	if !srv.concurrency.Acquire("t", "mock", "a") {
		t.Fatal("concurrency token still held after fail(); expected release")
	}
}

// TestReservationReleaseBeforeCreate covers the pre-job path: when creation
// itself fails, release() frees the scope and the pre-job token.
func TestReservationReleaseBeforeCreate(t *testing.T) {
	ctx := context.Background()
	srv := NewServer(Config{
		Version:     "test",
		AppSecret:   "dev-secret",
		ActorRole:   "service",
		TenantID:    "tenant_root",
		AppID:       "app_root",
		Concurrency: topology.NewConcurrencyRegistry(),
	})

	scope := idempotency.Scope{TenantID: "t", AppID: "a", Operation: "create_job", Key: "test-key-2"}
	res := srv.newJobReservation(scope, "t", "mock", "a")
	res.tokenAcquired = true
	res.release(ctx)

	dec, err := srv.idempotency.Reserve(ctx, scope, "hash-abc")
	if err != nil {
		t.Fatalf("re-reserve scope: %v", err)
	}
	if dec.Kind == idempotency.DecisionConflict {
		t.Fatal("idempotency scope still held after release(); expected release")
	}
	if !srv.concurrency.Acquire("t", "mock", "a") {
		t.Fatal("concurrency token still held after release(); expected release")
	}
}
