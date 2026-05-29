package webhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

type JobOutbox struct {
	Store       OutboxStore
	URLPolicy   URLPolicy
	MaxAttempts int
	Now         func() time.Time
}

func (o *JobOutbox) EnqueueTerminalJob(ctx context.Context, job jobstore.Job) error {
	if o == nil || o.Store == nil || !jobstore.TerminalStatus(job.Status) {
		return nil
	}
	callback, ok, err := CallbackFromMap(job.Callbacks, o.URLPolicy)
	if err != nil || !ok {
		return err
	}
	eventName := EventNameForJobStatus(job.Status)
	if !EventAllowed(callback, eventName) {
		return nil
	}
	payload, err := BuildJobWebhookPayload(job, eventName)
	if err != nil {
		return err
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	_, _, err = o.Store.Enqueue(ctx, EnqueueRequest{
		TenantID:      job.TenantID,
		AppID:         job.AppID,
		JobID:         job.ID,
		EventName:     eventName,
		EndpointKind:  "job_callback",
		URL:           callback.URL,
		SecretID:      callback.SecretID,
		DedupeKey:     fmt.Sprintf("job-terminal:%s:%s", job.ID, job.Status),
		Payload:       payload,
		TraceID:       job.TraceID,
		MaxAttempts:   normalizeMaxAttempts(o.MaxAttempts),
		NextAttemptAt: now().UTC(),
	})
	return err
}

func BuildJobWebhookPayload(job jobstore.Job, eventName string) ([]byte, error) {
	payload := map[string]any{
		"api_version": job.APIVersion,
		"event": map[string]any{
			"type":       eventName,
			"created_at": job.UpdatedAt.UTC().Format(time.RFC3339Nano),
		},
		"job": map[string]any{
			"job_id":       job.ID,
			"status":       string(job.Status),
			"target":       job.Target,
			"command_type": job.CommandType,
		},
		"trace_id": job.TraceID,
	}
	if job.RetryOf != "" {
		payload["job"].(map[string]any)["retry_of"] = job.RetryOf
	}
	return json.Marshal(payload)
}

func EventNameForJobStatus(status jobstore.Status) string {
	switch status {
	case jobstore.StatusCompleted:
		return "job.completed"
	case jobstore.StatusCompletedWithWarnings:
		return "job.completed_with_warnings"
	case jobstore.StatusFailedRetryable:
		return "job.failed_retryable"
	case jobstore.StatusFailedTerminal:
		return "job.failed"
	case jobstore.StatusDeadLetter:
		return "job.dead_lettered"
	case jobstore.StatusCanceled:
		return "job.cancelled"
	case jobstore.StatusTimedOut:
		return "job.timed_out"
	default:
		return "job." + string(status)
	}
}
