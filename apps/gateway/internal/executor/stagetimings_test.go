package executor

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

func TestMetricJobStagesMatchEventSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/shared-schemas/schemas/job-event.schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema struct {
		Properties struct {
			Data struct {
				Properties struct {
					Timings struct {
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"timings_ms"`
				} `json:"properties"`
			} `json:"data"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	got := map[string]bool{}
	for _, stage := range JobStages {
		got[stage] = true
	}
	want := map[string]bool{}
	for stage := range schema.Properties.Data.Properties.Timings.Properties {
		want[stage] = true
	}
	if len(want) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("JobStages = %v, schema timings_ms keys = %v", got, want)
	}
}

func TestMetricCollectStageTimings(t *testing.T) {
	events := []jobstore.WorkerEvent{
		{Data: map[string]any{"status": "running"}},
		{Data: map[string]any{"timings_ms": map[string]any{
			"browser_prep":    float64(1500),
			"provider_submit": json.Number("250.5"),
			"first_token":     int64(40),
			"extraction":      float64(-1),   // negative: dropped
			"bogus_stage":     float64(10),   // unknown: dropped
			"auth_check":      "12",          // non-numeric: dropped
			"worker_start":    float64(2e12), // implausible: dropped
		}}},
	}
	got := collectStageTimings(events)
	want := map[string]time.Duration{
		"browser_prep":    1500 * time.Millisecond,
		"provider_submit": 250500 * time.Microsecond,
		"first_token":     40 * time.Millisecond,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectStageTimings = %v, want %v", got, want)
	}
}

type stageRecordingMetrics struct {
	recordingWorkerMetrics
	stages map[string]time.Duration
	target string
}

func (m *stageRecordingMetrics) ObserveJobStage(target, stage string, d time.Duration) {
	if m.stages == nil {
		m.stages = map[string]time.Duration{}
	}
	m.target = target
	m.stages[stage] = d
}

func TestMetricWorkerConsumerRecordsStageTimings(t *testing.T) {
	store := jobstore.NewMemoryStore()
	dispatcher := NewFileSpoolDispatcher(t.TempDir())
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a",
		IdempotencyKey: "idem_stage_timings", Target: "mock", CommandType: "submit",
		Input: map[string]any{"prompt": "hello"}, TraceID: "trace_stage_timings",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := dispatcher.EnqueueJob(context.Background(), job); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	metrics := &stageRecordingMetrics{}
	consumer := WorkerConsumer{
		Spool: dispatcher, Jobs: store, Metrics: metrics,
		Runner: WorkerRunFunc(func(_ context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "evt_run", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "running", Sequence: 1, TraceID: envelope.TraceID, Data: map[string]any{"status": "running"}},
				{EventID: "evt_done", JobID: envelope.JobID, APIVersion: envelope.APIVersion, Type: "completed", Sequence: 2, TraceID: envelope.TraceID, Data: map[string]any{
					"status":     "completed",
					"result":     map[string]any{"type": "text", "text": "hi"},
					"timings_ms": map[string]any{"provider_submit": float64(120), "extraction": float64(8)},
				}},
			}, nil
		}),
	}
	if processed, err := consumer.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	if metrics.target != "mock" || metrics.stages["provider_submit"] != 120*time.Millisecond || metrics.stages["extraction"] != 8*time.Millisecond || len(metrics.stages) != 2 {
		t.Fatalf("stage observations = %v (target %q)", metrics.stages, metrics.target)
	}
}
