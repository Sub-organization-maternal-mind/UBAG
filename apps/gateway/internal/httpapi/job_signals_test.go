package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

func TestJobSignalsFromEvents(t *testing.T) {
	tests := []struct {
		name        string
		events      []jobstore.Event
		wantClass   string
		wantMessage string
		wantManual  string
	}{
		{
			name:   "no signals",
			events: []jobstore.Event{{Type: "queued"}, {Type: "running"}, {Type: "completed"}},
		},
		{
			name: "failed carries class and message",
			events: []jobstore.Event{
				{Type: "running", Data: map[string]any{"status": "running"}},
				{Type: "failed", Data: map[string]any{"error_class": "worker_execution", "message": "boom"}},
			},
			wantClass:   "worker_execution",
			wantMessage: "boom",
		},
		{
			name: "latest failure wins",
			events: []jobstore.Event{
				{Type: "failed_retryable", Data: map[string]any{"error_class": "transient", "message": "first"}},
				{Type: "failed_terminal", Data: map[string]any{"error_class": "fatal", "message": "second"}},
			},
			wantClass:   "fatal",
			wantMessage: "second",
		},
		{
			name: "manual action prefers message over reason",
			events: []jobstore.Event{
				{Type: "session.manual_action_required", Data: map[string]any{"reason": "login_required", "message": "Sign in to ChatGPT"}},
			},
			wantManual: "Sign in to ChatGPT",
		},
		{
			name: "manual action falls back to reason",
			events: []jobstore.Event{
				{Type: "session.manual_action_required", Data: map[string]any{"reason": "captcha_required"}},
			},
			wantManual: "captcha_required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jobSignalsFromEvents(tt.events)
			if got.ErrorClass != tt.wantClass || got.ErrorMessage != tt.wantMessage || got.ManualAction != tt.wantManual {
				t.Fatalf("jobSignalsFromEvents = %+v; want class=%q message=%q manual=%q",
					got, tt.wantClass, tt.wantMessage, tt.wantManual)
			}
		})
	}
}

func TestGetJobExposesLastErrorFromWorkerEvents(t *testing.T) {
	store := jobstore.NewMemoryStore()
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store}).Handler()
	id := createSignalsJob(t, server, "idem_signal_error")

	job := loadStoredJob(t, store, id)
	applySignalsEvent(t, store, jobstore.WorkerEvent{
		EventID:    "signal_failed_1",
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       "failed",
		Sequence:   2,
		TraceID:    job.TraceID,
		Data: map[string]any{
			"status":      "failed_retryable",
			"retryable":   true,
			"error_class": "worker_execution",
			"message":     "browser session crashed",
		},
	})

	body := readJobJSON(t, server, id)
	if body["error"] != "browser session crashed" {
		t.Fatalf("error = %v, want %q", body["error"], "browser session crashed")
	}
	if body["error_class"] != "worker_execution" {
		t.Fatalf("error_class = %v, want %q", body["error_class"], "worker_execution")
	}
	// The manual_action field must stay absent for a plain failure.
	if _, ok := body["manual_action"]; ok {
		t.Fatalf("manual_action should be omitted, got %v", body["manual_action"])
	}
}

func TestGetJobExposesManualActionFromWorkerEvents(t *testing.T) {
	store := jobstore.NewMemoryStore()
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store}).Handler()
	id := createSignalsJob(t, server, "idem_signal_manual")

	job := loadStoredJob(t, store, id)
	applySignalsEvent(t, store, jobstore.WorkerEvent{
		EventID:    "signal_manual_1",
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       "session.manual_action_required",
		Sequence:   2,
		TraceID:    job.TraceID,
		Data: map[string]any{
			"reason":  "login_required",
			"message": "Sign in to ChatGPT in the live browser",
		},
	})

	body := readJobJSON(t, server, id)
	if body["manual_action"] != "Sign in to ChatGPT in the live browser" {
		t.Fatalf("manual_action = %v, want %q", body["manual_action"], "Sign in to ChatGPT in the live browser")
	}
}

func TestGetJobSuppressesManualActionOnceTerminal(t *testing.T) {
	store := jobstore.NewMemoryStore()
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store}).Handler()
	id := createSignalsJob(t, server, "idem_signal_terminal")

	job := loadStoredJob(t, store, id)
	applySignalsEvent(t, store, jobstore.WorkerEvent{
		EventID:    "signal_manual_then_fail_1",
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       "session.manual_action_required",
		Sequence:   2,
		TraceID:    job.TraceID,
		Data:       map[string]any{"reason": "login_required", "message": "Sign in to ChatGPT"},
	})
	applySignalsEvent(t, store, jobstore.WorkerEvent{
		EventID:    "signal_manual_then_fail_2",
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       "failed",
		Sequence:   3,
		TraceID:    job.TraceID,
		Data:       map[string]any{"status": "failed_retryable", "retryable": true, "error_class": "worker_execution", "message": "boom"},
	})

	body := readJobJSON(t, server, id)
	if body["error"] != "boom" || body["error_class"] != "worker_execution" {
		t.Fatalf("expected terminal error to be surfaced, got error=%v error_class=%v", body["error"], body["error_class"])
	}
	// The earlier manual-action prompt is moot once the job is terminally failed.
	if _, ok := body["manual_action"]; ok {
		t.Fatalf("manual_action should be suppressed for a terminal job, got %v", body["manual_action"])
	}
}

func TestGetJobOmitsSignalsForCleanJob(t *testing.T) {
	store := jobstore.NewMemoryStore()
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store}).Handler()
	id := createSignalsJob(t, server, "idem_signal_clean")

	body := readJobJSON(t, server, id)
	if _, ok := body["error"]; ok {
		t.Fatalf("error should be omitted for a clean job, got %v", body["error"])
	}
	if _, ok := body["error_class"]; ok {
		t.Fatalf("error_class should be omitted for a clean job, got %v", body["error_class"])
	}
	if _, ok := body["manual_action"]; ok {
		t.Fatalf("manual_action should be omitted for a clean job, got %v", body["manual_action"])
	}
}

func createSignalsJob(t *testing.T, server http.Handler, idem string) string {
	t.Helper()
	body := `{"api_version":"2026-05-22","idempotency_key":"` + idem + `","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{}}}`
	create := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders(idem))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created.JobID
}

func loadStoredJob(t *testing.T, store *jobstore.MemoryStore, id string) jobstore.Job {
	t.Helper()
	job, found, err := store.Get(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("store.Get(%s) found=%v err=%v", id, found, err)
	}
	return job
}

func applySignalsEvent(t *testing.T, store *jobstore.MemoryStore, event jobstore.WorkerEvent) {
	t.Helper()
	if _, found, err := store.ApplyWorkerEvent(context.Background(), event); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent(%s) found=%v err=%v", event.Type, found, err)
	}
}

func readJobJSON(t *testing.T, server http.Handler, id string) map[string]any {
	t.Helper()
	get := doJSON(server, http.MethodGet, "/v1/jobs/"+id, "", authHeaders(""))
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d; body=%s", get.Code, get.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(get.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	return body
}
