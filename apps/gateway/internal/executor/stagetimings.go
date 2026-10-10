package executor

import (
	"encoding/json"
	"math"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// JobStages is the closed stage set of data.timings_ms. It mirrors
// job-event.schema.json and observability JOB_STAGES (a test pins the schema).
var JobStages = []string{
	"provider_config",
	"worker_start", "browser_prep", "auth_check", "attachment_materialize",
	"provider_submit", "first_token", "provider_stream", "extraction",
}

// IsJobStage reports whether stage is in the closed JobStages set.
func IsJobStage(stage string) bool {
	for _, known := range JobStages {
		if stage == known {
			return true
		}
	}
	return false
}

// StageMetricsRecorder is the optional WorkerMetricsRecorder capability that
// receives worker-reported stage durations. It is probed with a type
// assertion so existing recorders need no change.
type StageMetricsRecorder interface {
	ObserveJobStage(target, stage string, duration time.Duration)
}

// maxStageMillis drops absurd values (a day) rather than poisoning the sum.
const maxStageMillis = 24 * 60 * 60 * 1000

// collectStageTimings merges data.timings_ms from a run's events (later events
// win) into stage -> duration. Unknown keys, non-numeric, negative, non-finite
// or implausibly large values are ignored.
func collectStageTimings(events []jobstore.WorkerEvent) map[string]time.Duration {
	var out map[string]time.Duration
	for _, event := range events {
		raw, ok := event.Data["timings_ms"].(map[string]any)
		if !ok {
			continue
		}
		for stage, value := range raw {
			if !IsJobStage(stage) {
				continue
			}
			var ms float64
			switch v := value.(type) {
			case float64:
				ms = v
			case int:
				ms = float64(v)
			case int64:
				ms = float64(v)
			case json.Number:
				parsed, err := v.Float64()
				if err != nil {
					continue
				}
				ms = parsed
			default:
				continue
			}
			if math.IsNaN(ms) || ms < 0 || ms > maxStageMillis {
				continue
			}
			if out == nil {
				out = map[string]time.Duration{}
			}
			out[stage] = time.Duration(ms * float64(time.Millisecond))
		}
	}
	return out
}

func (c *WorkerConsumer) observeStageTimings(target string, events []jobstore.WorkerEvent) {
	if c == nil || c.Metrics == nil {
		return
	}
	recorder, ok := c.Metrics.(StageMetricsRecorder)
	if !ok {
		return
	}
	for stage, duration := range collectStageTimings(events) {
		recorder.ObserveJobStage(target, stage, duration)
	}
}
