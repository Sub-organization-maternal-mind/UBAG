package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

func seedSummaryJob(t *testing.T, store jobstore.Store, tenant string, n int, status jobstore.Status) jobstore.Job {
	t.Helper()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: DefaultAPIVersion, TenantID: tenant, AppID: defaultAppID,
		IdempotencyKey: fmt.Sprintf("idem_summary_%s_%s_%d", tenant, status, n),
		Target:         "mock_target", CommandType: "echo",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != job.Status {
		if job, _, err = store.UpdateStatus(context.Background(), job.ID, status); err != nil {
			t.Fatalf("update status: %v", err)
		}
	}
	return job
}

func TestJobsSummaryCountsPastListCap(t *testing.T) {
	store := jobstore.NewMemoryStore()
	first := seedSummaryJob(t, store, defaultTenantID, 0, jobstore.StatusQueued)
	for i := 1; i < 130; i++ { // > the 100-row list page cap
		seedSummaryJob(t, store, defaultTenantID, i, jobstore.StatusQueued)
	}
	seedSummaryJob(t, store, defaultTenantID, 0, jobstore.StatusFailedTerminal)
	seedSummaryJob(t, store, "other-tenant", 0, jobstore.StatusQueued) // must not be counted

	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store}).Handler()
	resp := doJSON(server, http.MethodGet, "/v1/jobs/summary", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Kind           string         `json:"kind"`
		Total          int            `json:"total"`
		CountsByStatus map[string]int `json:"counts_by_status"`
		QueuedByReason map[string]int `json:"queued_by_reason"`
		OldestQueuedAt *string        `json:"oldest_queued_at"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Kind != "jobs_summary" || body.Total != 131 || body.CountsByStatus["queued"] != 130 || body.CountsByStatus["failed_terminal"] != 1 {
		t.Fatalf("unexpected summary: %+v", body)
	}
	if body.QueuedByReason == nil || len(body.QueuedByReason) != 0 {
		t.Fatalf("queued_by_reason must be an empty object: %+v", body.QueuedByReason)
	}
	if body.OldestQueuedAt == nil || *body.OldestQueuedAt == "" {
		t.Fatalf("oldest_queued_at missing: %+v", body)
	}
	if want := first.CreatedAt.UTC().Format("2006-01-02T15:04:05"); (*body.OldestQueuedAt)[:19] != want {
		t.Fatalf("oldest_queued_at = %s, want prefix %s", *body.OldestQueuedAt, want)
	}
}

func TestJobsSummaryEmptyAndMethodGuard(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: jobstore.NewMemoryStore()}).Handler()
	resp := doJSON(server, http.MethodGet, "/v1/jobs/summary", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, present := body["oldest_queued_at"]; !present || v != nil {
		t.Fatalf("oldest_queued_at = %v (present=%v), want null", v, present)
	}
	if resp := doJSON(server, http.MethodPost, "/v1/jobs/summary", "{}", authHeaders("")); resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", resp.Code)
	}
	if got := routePattern("/v1/jobs/summary"); got != "/v1/jobs/summary" {
		t.Fatalf("routePattern = %s", got)
	}
}
