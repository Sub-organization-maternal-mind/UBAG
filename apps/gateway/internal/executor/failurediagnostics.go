package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
)

// Classify trusted boundary errors into fixed public tokens; never persist the
// exception text, stderr, provider response or command arguments.
func workerFailureClass(err error) (class, stage string) {
	var exit *exec.ExitError
	var syntax *json.SyntaxError
	var ingest *streamIngestError
	switch {
	case errors.Is(err, ErrAmbiguous):
		return "post_submit_ambiguous", "provider_interaction"
	case errors.Is(err, context.DeadlineExceeded):
		return "worker_deadline", "worker_execution"
	case errors.As(err, &exit):
		return "worker_process_exit", "worker_execution"
	case errors.As(err, &syntax), errors.As(err, &ingest):
		return "worker_protocol", "event_ingestion"
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	for _, prefix := range []string{"worker daemon ended without", "worker daemon terminal marker", "worker daemon stdout exceeded", "worker emitted no events", "decode worker", "invalid worker event"} {
		if strings.HasPrefix(message, prefix) {
			return "worker_protocol", "event_ingestion"
		}
	}
	if strings.HasPrefix(message, "worker process timed out") {
		return "worker_deadline", "worker_execution"
	}
	if strings.HasPrefix(message, "start worker") {
		return "worker_process_start", "worker_start"
	}
	if strings.HasPrefix(message, "worker daemon job failed") {
		return "worker_reported_failure", "worker_execution"
	}
	return "worker_execution", "worker_execution"
}
