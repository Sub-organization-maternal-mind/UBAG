package executor

import (
	"context"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

func reaperTestJob(t *testing.T, store *jobstore.MemoryStore, options map[string]any, notBefore *time.Time) jobstore.Job {
	t.Helper()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    "tenant_a",
		AppID:       "app_a",
		Target:      "mock",
		CommandType: "submit",
		Input:       map[string]any{"prompt": "hi"},
		Options:     options,
		NotBefore:   notBefore,
		TraceID:     "trace_reaper",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return job
}

func TestStaleJobReaperTimesOutStuckJobAndReleasesToken(t *testing.T) {
	store := jobstore.NewMemoryStore()
	registry := topology.NewConcurrencyRegistry()
	registry.Report("tenant_a", topology.ConcurrencyView{Target: "mock", IdentityRef: "app_a", CurrentCap: 1})

	job := reaperTestJob(t, store, nil, nil)
	// Model the gateway having acquired this job's lane token at creation.
	if !registry.Acquire("tenant_a", "mock", "app_a") {
		t.Fatal("precondition: acquire should succeed")
	}

	rp := &StaleJobReaper{
		Jobs:        store,
		Concurrency: registry,
		MaxLifetime: time.Minute,
		Now:         func() time.Time { return job.CreatedAt.Add(5 * time.Minute) }, // long idle
	}
	n, err := rp.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped = %d, want 1", n)
	}
	got, found, _ := store.Get(context.Background(), job.ID)
	if !found || got.Status != jobstore.StatusTimedOut {
		t.Fatalf("status = %s (found=%v), want timed_out", got.Status, found)
	}
	// The token must have been released: the cap-1 lane has a free slot again.
	if !registry.Acquire("tenant_a", "mock", "app_a") {
		t.Fatal("reaper timed out the job but did not release its concurrency token")
	}
}

func TestStaleJobReaperLeavesFreshJob(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job := reaperTestJob(t, store, nil, nil)
	rp := &StaleJobReaper{
		Jobs:        store,
		MaxLifetime: time.Minute,
		Now:         func() time.Time { return job.CreatedAt.Add(15 * time.Second) }, // within lifetime
	}
	n, err := rp.SweepOnce(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("reaped = %d err = %v, want 0 / nil", n, err)
	}
	got, _, _ := store.Get(context.Background(), job.ID)
	if got.Status == jobstore.StatusTimedOut {
		t.Fatal("reaped a job that was still within its lifetime")
	}
}

func TestStaleJobReaperRespectsFutureNotBefore(t *testing.T) {
	store := jobstore.NewMemoryStore()
	base := reaperTestJob(t, store, nil, nil) // just to anchor a stable clock
	future := base.CreatedAt.Add(time.Hour)
	scheduled := reaperTestJob(t, store, nil, &future)

	rp := &StaleJobReaper{
		Jobs:        store,
		MaxLifetime: time.Minute,
		// Well past the idle deadline, but still before the scheduled NotBefore.
		Now: func() time.Time { return scheduled.CreatedAt.Add(10 * time.Minute) },
	}
	if _, err := rp.SweepOnce(context.Background()); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	got, _, _ := store.Get(context.Background(), scheduled.ID)
	if got.Status == jobstore.StatusTimedOut {
		t.Fatal("reaped a job still waiting on its future NotBefore schedule")
	}
}

func TestStaleJobReaperEnforcesExplicitTimeoutSeconds(t *testing.T) {
	store := jobstore.NewMemoryStore()
	// No MaxLifetime fallback: only the explicit per-job budget may reap.
	job := reaperTestJob(t, store, map[string]any{"timeout_seconds": float64(30)}, nil)
	rp := &StaleJobReaper{
		Jobs:        store,
		MaxLifetime: 0,
		Now:         func() time.Time { return job.CreatedAt.Add(45 * time.Second) }, // past 30s budget
	}
	n, err := rp.SweepOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("reaped = %d err = %v, want 1 / nil", n, err)
	}
	got, _, _ := store.Get(context.Background(), job.ID)
	if got.Status != jobstore.StatusTimedOut {
		t.Fatalf("status = %s, want timed_out (timeout_seconds exceeded)", got.Status)
	}
}
