package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// promptSubmittedEventType is the worker event the engine emits right after the
// prompt is submitted to the provider (UBAG_WORKER_STRICT_SUBMIT, worker side).
const promptSubmittedEventType = "prompt_submitted"

// ErrNotSubmitted marks a runner failure that happened before the prompt
// reached the provider: the interaction can safely be attempted again.
// ErrAmbiguous marks a failure after submission: the provider may already hold
// the turn, so the job must never be replayed and ends failed for reconcile.
// Both are only produced with UBAG_WORKER_STRICT_SUBMIT on (default off).
var (
	ErrNotSubmitted = errors.New("worker failed before prompt submission")
	ErrAmbiguous    = errors.New("worker failed after prompt submission")
)

// strictSubmitEnabled gates the submission boundary (default off).
func strictSubmitEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_WORKER_STRICT_SUBMIT"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// classifySubmission types a runner error from the submission state observed in
// process. The cause stays in the chain (context.Canceled etc. still match).
// ponytail: submitted is the in-process marker only; a crash that loses the
// marker reads as ErrNotSubmitted until the durable attempt ledger (P3.8).
func classifySubmission(err error, submitted bool) error {
	if err == nil || !strictSubmitEnabled() ||
		errors.Is(err, ErrNotSubmitted) || errors.Is(err, ErrAmbiguous) {
		return err
	}
	if submitted {
		return fmt.Errorf("%w: %w", ErrAmbiguous, err)
	}
	return fmt.Errorf("%w: %w", ErrNotSubmitted, err)
}

// lineIsPromptSubmitted reports whether one JSONL line is the prompt_submitted event.
func lineIsPromptSubmitted(line string) bool {
	if !strings.Contains(line, promptSubmittedEventType) {
		return false
	}
	var event struct {
		Type string `json:"type"`
	}
	return json.Unmarshal([]byte(line), &event) == nil && event.Type == promptSubmittedEventType
}

// jsonlHasPromptSubmitted scans buffered worker stdout for the marker.
func jsonlHasPromptSubmitted(body []byte) bool {
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		if lineIsPromptSubmitted(string(bytes.TrimSpace(line))) {
			return true
		}
	}
	return false
}

// ambiguousIfSubmitted wraps err as ErrAmbiguous when strict mode is on and the
// worker's own events show the prompt was submitted; used by the ingestion
// failure branches, where the runner returned events but they are unusable.
func ambiguousIfSubmitted(err error, events []jobstore.WorkerEvent) error {
	if err == nil || !strictSubmitEnabled() {
		return err
	}
	for _, event := range events {
		if event.Type == promptSubmittedEventType {
			return classifySubmission(err, true)
		}
	}
	return err
}

// detachedOpContext outlives a cancelled parent (gateway shutdown) so the
// fail-closed write after submission still lands.
func detachedOpContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}
