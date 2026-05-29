package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

const (
	defaultWorkerPollInterval = 500 * time.Millisecond
	defaultWorkerMaxRuntime   = 30 * time.Second
	maxWorkerOutputBytes      = 1024 * 1024
	maxWorkerStderrBytes      = 8 * 1024
	maxWorkerEvents           = 512
)

type WorkerConsumer struct {
	Queue            WorkerQueue
	Spool            *FileSpoolDispatcher
	Jobs             jobstore.Store
	Runner           WorkerRunner
	TerminalNotifier TerminalJobNotifier
	PollInterval     time.Duration
}

type WorkerQueue interface {
	Ready(ctx context.Context) error
	LeaseNext(ctx context.Context) (WorkerLease, bool, error)
}

type WorkerLease interface {
	JobID() string
	LeaseID() string
	QueueName() string
	Envelope() DispatchEnvelope
	Complete(ctx context.Context) error
	Fail(ctx context.Context) error
	Cancel(ctx context.Context) error
	Retry(ctx context.Context) error
	Poison(ctx context.Context, reason string) error
}

type WorkerRunner interface {
	RunWorker(ctx context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error)
}

type TerminalJobNotifier interface {
	EnqueueTerminalJob(ctx context.Context, job jobstore.Job) error
}

type WorkerRunFunc func(ctx context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error)

func (f WorkerRunFunc) RunWorker(ctx context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
	return f(ctx, envelope)
}

type ProcessWorkerRunner struct {
	Python     string
	Script     string
	MaxRuntime time.Duration
}

func NewFileSpoolWorkerQueue(spool *FileSpoolDispatcher) WorkerQueue {
	return fileSpoolWorkerQueue{spool: spool}
}

func (c *WorkerConsumer) Ready(ctx context.Context) error {
	queue, err := c.workerQueue()
	if err != nil {
		return err
	}
	return queue.Ready(ctx)
}

func (c *WorkerConsumer) Run(ctx context.Context) error {
	pollInterval := c.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultWorkerPollInterval
	}
	for {
		processed, err := c.RunOnce(ctx)
		if err != nil {
			return err
		}
		if processed {
			continue
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *WorkerConsumer) RunOnce(ctx context.Context) (bool, error) {
	if c.Jobs == nil {
		return false, fmt.Errorf("worker consumer job store is not configured")
	}
	if c.Runner == nil {
		return false, fmt.Errorf("worker consumer runner is not configured")
	}
	queue, err := c.workerQueue()
	if err != nil {
		return false, err
	}

	lease, ok, err := queue.LeaseNext(ctx)
	if err != nil || !ok {
		return ok, err
	}
	if lease == nil {
		return true, nil
	}

	job, found, err := c.Jobs.Get(ctx, lease.JobID())
	if err != nil {
		_ = lease.Retry(ctx)
		return true, err
	}
	if !found {
		_ = lease.Poison(ctx, "leased job does not exist in job store")
		return true, fmt.Errorf("leased job %s does not exist in job store", lease.JobID())
	}
	if err := validateLeaseEnvelope(job, lease.Envelope()); err != nil {
		_ = lease.Poison(ctx, "lease envelope does not match persisted job")
		return true, err
	}
	envelope := EnvelopeFromJob(job)
	if jobstore.TerminalStatus(job.Status) {
		if err := c.notifyTerminalJob(ctx, lease, job); err != nil {
			return true, err
		}
		if job.Status == jobstore.StatusCanceled {
			return true, lease.Cancel(ctx)
		}
		return true, lease.Complete(ctx)
	}
	assignedJob, found, err := c.Jobs.UpdateStatus(ctx, job.ID, jobstore.StatusAssigned)
	if err != nil {
		_ = lease.Retry(ctx)
		return true, err
	}
	if !found {
		_ = lease.Poison(ctx, "leased job disappeared before assignment")
		return true, fmt.Errorf("leased job %s does not exist in job store", lease.JobID())
	}
	if jobstore.TerminalStatus(assignedJob.Status) {
		if err := c.notifyTerminalJob(ctx, lease, assignedJob); err != nil {
			return true, err
		}
		if assignedJob.Status == jobstore.StatusCanceled {
			return true, lease.Cancel(ctx)
		}
		return true, lease.Complete(ctx)
	}

	events, err := c.runWorkerWithCancellation(ctx, envelope)
	if err != nil {
		if ctx.Err() != nil {
			_ = lease.Retry(ctx)
			return true, err
		}
		if errors.Is(err, context.Canceled) {
			finalJob, found, finalErr := c.Jobs.Get(context.Background(), job.ID)
			if finalErr != nil {
				_ = lease.Retry(ctx)
				return true, finalErr
			}
			if found && finalJob.Status == jobstore.StatusCanceled {
				return true, lease.Cancel(ctx)
			}
			_ = lease.Retry(ctx)
			return true, err
		}
		if applyErr := c.applyFailure(ctx, lease, envelope, err); applyErr != nil {
			_ = lease.Retry(ctx)
			return true, applyErr
		}
		if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
			return true, notifyErr
		}
		return true, lease.Fail(ctx)
	}
	if len(events) == 0 {
		if applyErr := c.applyFailure(ctx, lease, envelope, fmt.Errorf("worker emitted no events")); applyErr != nil {
			_ = lease.Retry(ctx)
			return true, applyErr
		}
		if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
			return true, notifyErr
		}
		return true, lease.Fail(ctx)
	}

	for _, event := range events {
		normalized, err := normalizeWorkerEvent(envelope, event)
		if err != nil {
			if applyErr := c.applyFailure(ctx, lease, envelope, err); applyErr != nil {
				_ = lease.Retry(ctx)
				return true, applyErr
			}
			if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
				return true, notifyErr
			}
			return true, lease.Fail(ctx)
		}
		if _, found, err := c.Jobs.ApplyWorkerEvent(ctx, normalized); err != nil {
			if applyErr := c.applyFailure(ctx, lease, envelope, err); applyErr != nil {
				_ = lease.Retry(ctx)
				return true, applyErr
			}
			if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
				return true, notifyErr
			}
			return true, lease.Fail(ctx)
		} else if !found {
			_ = lease.Poison(ctx, "worker event referenced missing job")
			return true, fmt.Errorf("worker event referenced missing job %s", normalized.JobID)
		}
	}

	finalJob, found, err := c.Jobs.Get(ctx, lease.JobID())
	if err != nil {
		_ = lease.Retry(ctx)
		return true, err
	}
	if !found {
		_ = lease.Poison(ctx, "job disappeared during worker ingestion")
		return true, fmt.Errorf("job %s disappeared during worker ingestion", lease.JobID())
	}
	if finalJob.Status == jobstore.StatusCanceled {
		if err := c.notifyTerminalJob(ctx, lease, finalJob); err != nil {
			return true, err
		}
		return true, lease.Cancel(ctx)
	}
	if finalJob.Status == jobstore.StatusCompleted || finalJob.Status == jobstore.StatusCompletedWithWarnings {
		if err := c.notifyTerminalJob(ctx, lease, finalJob); err != nil {
			return true, err
		}
		return true, lease.Complete(ctx)
	}
	if jobstore.TerminalStatus(finalJob.Status) {
		if err := c.notifyTerminalJob(ctx, lease, finalJob); err != nil {
			return true, err
		}
		return true, lease.Fail(ctx)
	}

	if applyErr := c.applyFailure(ctx, lease, envelope, fmt.Errorf("worker did not emit a terminal event")); applyErr != nil {
		_ = lease.Retry(ctx)
		return true, applyErr
	}
	if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
		return true, notifyErr
	}
	return true, lease.Fail(ctx)
}

func (c *WorkerConsumer) workerQueue() (WorkerQueue, error) {
	if c == nil {
		return nil, fmt.Errorf("worker consumer is not configured")
	}
	if c.Queue != nil {
		return c.Queue, nil
	}
	if c.Spool != nil {
		return NewFileSpoolWorkerQueue(c.Spool), nil
	}
	return nil, fmt.Errorf("worker consumer queue is not configured")
}

func (c *WorkerConsumer) runWorkerWithCancellation(ctx context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				job, found, err := c.Jobs.Get(runCtx, envelope.JobID)
				if err != nil || !found {
					continue
				}
				if job.Status == jobstore.StatusCanceled {
					cancel()
					return
				}
			}
		}
	}()

	events, err := c.Runner.RunWorker(runCtx, envelope)
	cancel()
	<-done
	return events, err
}

func (c *WorkerConsumer) applyFailure(ctx context.Context, lease WorkerLease, envelope DispatchEnvelope, cause error) error {
	data := map[string]any{
		"status":      string(jobstore.StatusFailedRetryable),
		"retryable":   true,
		"error_class": "worker_execution",
		"message":     sanitizeWorkerError(cause),
	}
	event := jobstore.WorkerEvent{
		EventID:    "gateway_worker_failure:" + lease.JobID() + ":" + lease.LeaseID(),
		JobID:      lease.JobID(),
		APIVersion: envelope.APIVersion,
		Type:       "failed",
		TraceID:    envelope.TraceID,
		Data:       data,
		CreatedAt:  time.Now().UTC(),
	}
	_, found, err := c.Jobs.ApplyWorkerEvent(ctx, event)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("failed worker event referenced missing job %s", lease.JobID())
	}
	return nil
}

func (c *WorkerConsumer) notifyCurrentTerminalJob(ctx context.Context, lease WorkerLease) error {
	if c.TerminalNotifier == nil {
		return nil
	}
	job, found, err := c.Jobs.Get(ctx, lease.JobID())
	if err != nil {
		_ = lease.Retry(ctx)
		return err
	}
	if !found {
		_ = lease.Poison(ctx, "terminal notification referenced missing job")
		return fmt.Errorf("terminal notification referenced missing job %s", lease.JobID())
	}
	return c.notifyTerminalJob(ctx, lease, job)
}

func (c *WorkerConsumer) notifyTerminalJob(ctx context.Context, lease WorkerLease, job jobstore.Job) error {
	if c.TerminalNotifier == nil || !jobstore.TerminalStatus(job.Status) {
		return nil
	}
	if err := c.TerminalNotifier.EnqueueTerminalJob(ctx, job); err != nil {
		_ = lease.Retry(ctx)
		return err
	}
	return nil
}

func (r ProcessWorkerRunner) RunWorker(ctx context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
	python := strings.TrimSpace(r.Python)
	if python == "" {
		python = "python"
	}
	script := strings.TrimSpace(r.Script)
	if script == "" {
		return nil, fmt.Errorf("worker script is not configured")
	}
	maxRuntime := r.MaxRuntime
	if maxRuntime <= 0 {
		maxRuntime = defaultWorkerMaxRuntime
	}

	runCtx, cancel := context.WithTimeout(ctx, maxRuntime)
	defer cancel()

	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}

	command := exec.CommandContext(runCtx, python, script, "--input", "-")
	command.Stdin = bytes.NewReader(payload)
	stdout := &limitedBuffer{max: maxWorkerOutputBytes}
	stderr := &limitedBuffer{max: maxWorkerStderrBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = minimalWorkerEnv()
	if err := command.Run(); err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("worker process timed out after %s", maxRuntime)
		}
		if runCtx.Err() == context.Canceled {
			return nil, context.Canceled
		}
		return nil, fmt.Errorf("worker process failed")
	}
	if stdout.truncated {
		return nil, fmt.Errorf("worker stdout exceeded %d bytes", maxWorkerOutputBytes)
	}
	return parseWorkerJSONL(stdout.Bytes())
}

func parseWorkerJSONL(output []byte) ([]jobstore.WorkerEvent, error) {
	if len(output) > maxWorkerOutputBytes {
		return nil, fmt.Errorf("worker output exceeds %d bytes", maxWorkerOutputBytes)
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	events := []jobstore.WorkerEvent{}
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event jobstore.WorkerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("worker emitted malformed JSONL at line %d", lineNumber)
		}
		if event.Type == "" {
			return nil, fmt.Errorf("worker event at line %d is missing type", lineNumber)
		}
		events = append(events, event)
		if len(events) > maxWorkerEvents {
			return nil, fmt.Errorf("worker emitted more than %d events", maxWorkerEvents)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func normalizeWorkerEvent(envelope DispatchEnvelope, event jobstore.WorkerEvent) (jobstore.WorkerEvent, error) {
	if strings.TrimSpace(event.EventID) == "" && event.Sequence <= 0 {
		return jobstore.WorkerEvent{}, fmt.Errorf("worker event must include event_id or positive sequence")
	}
	if event.JobID == "" {
		event.JobID = envelope.JobID
	}
	if event.JobID != envelope.JobID {
		return jobstore.WorkerEvent{}, fmt.Errorf("worker event job_id %q does not match leased job_id %q", event.JobID, envelope.JobID)
	}
	if event.APIVersion == "" {
		event.APIVersion = envelope.APIVersion
	}
	if event.APIVersion != envelope.APIVersion {
		return jobstore.WorkerEvent{}, fmt.Errorf("worker event api_version %q does not match leased api_version %q", event.APIVersion, envelope.APIVersion)
	}
	if event.TraceID == "" {
		event.TraceID = envelope.TraceID
	}
	if event.TraceID != envelope.TraceID {
		return jobstore.WorkerEvent{}, fmt.Errorf("worker event trace_id %q does not match leased trace_id %q", event.TraceID, envelope.TraceID)
	}
	if event.Data == nil {
		event.Data = map[string]any{}
	}
	return event, nil
}

type fileSpoolWorkerQueue struct {
	spool *FileSpoolDispatcher
}

func (q fileSpoolWorkerQueue) Ready(ctx context.Context) error {
	if q.spool == nil {
		return fmt.Errorf("file spool worker queue is not configured")
	}
	return q.spool.Ready(ctx)
}

func (q fileSpoolWorkerQueue) LeaseNext(ctx context.Context) (WorkerLease, bool, error) {
	if q.spool == nil {
		return nil, false, fmt.Errorf("file spool worker queue is not configured")
	}
	lease, ok, err := q.spool.LeaseNext(ctx)
	if err != nil || !ok {
		return nil, ok, err
	}
	return fileSpoolWorkerLease{spool: q.spool, lease: lease}, true, nil
}

type fileSpoolWorkerLease struct {
	spool *FileSpoolDispatcher
	lease FileSpoolLease
}

func (l fileSpoolWorkerLease) JobID() string {
	return l.lease.JobID
}

func (l fileSpoolWorkerLease) LeaseID() string {
	return l.lease.LeaseID
}

func (l fileSpoolWorkerLease) QueueName() string {
	return "file-spool"
}

func (l fileSpoolWorkerLease) Envelope() DispatchEnvelope {
	return l.lease.Envelope
}

func (l fileSpoolWorkerLease) Complete(ctx context.Context) error {
	return l.spool.CompleteLease(ctx, l.lease)
}

func (l fileSpoolWorkerLease) Fail(ctx context.Context) error {
	return l.spool.FailLease(ctx, l.lease)
}

func (l fileSpoolWorkerLease) Cancel(ctx context.Context) error {
	return l.spool.CancelLease(ctx, l.lease)
}

func (l fileSpoolWorkerLease) Retry(ctx context.Context) error {
	return l.spool.RetryLease(ctx, l.lease)
}

func (l fileSpoolWorkerLease) Poison(ctx context.Context, _ string) error {
	return l.spool.FailLease(ctx, l.lease)
}

func validateLeaseEnvelope(job jobstore.Job, envelope DispatchEnvelope) error {
	expected := EnvelopeFromJob(job)
	if envelope.APIVersion != expected.APIVersion {
		return fmt.Errorf("leased job %s has api version %q, expected %q", job.ID, envelope.APIVersion, expected.APIVersion)
	}
	if envelope.JobID != expected.JobID {
		return fmt.Errorf("leased job %s has envelope job id %q", job.ID, envelope.JobID)
	}
	if envelope.TenantID != expected.TenantID {
		return fmt.Errorf("leased job %s has tenant id %q, expected %q", job.ID, envelope.TenantID, expected.TenantID)
	}
	if envelope.AppID != expected.AppID {
		return fmt.Errorf("leased job %s has app id %q, expected %q", job.ID, envelope.AppID, expected.AppID)
	}
	if envelope.IdempotencyKey != expected.IdempotencyKey {
		return fmt.Errorf("leased job %s has idempotency key %q, expected %q", job.ID, envelope.IdempotencyKey, expected.IdempotencyKey)
	}
	if envelope.TraceID != expected.TraceID {
		return fmt.Errorf("leased job %s has trace id %q, expected %q", job.ID, envelope.TraceID, expected.TraceID)
	}
	if envelope.RetryOf != expected.RetryOf {
		return fmt.Errorf("leased job %s has retry_of %q, expected %q", job.ID, envelope.RetryOf, expected.RetryOf)
	}
	if envelope.Job.Target != expected.Job.Target {
		return fmt.Errorf("leased job %s has target %q, expected %q", job.ID, envelope.Job.Target, expected.Job.Target)
	}
	if envelope.Job.CommandType != expected.Job.CommandType {
		return fmt.Errorf("leased job %s has command type %q, expected %q", job.ID, envelope.Job.CommandType, expected.Job.CommandType)
	}
	if envelope.Job.ConversationID != expected.Job.ConversationID {
		return fmt.Errorf("leased job %s has conversation id %q, expected %q", job.ID, envelope.Job.ConversationID, expected.Job.ConversationID)
	}
	if envelope.Job.TemplateID != expected.Job.TemplateID {
		return fmt.Errorf("leased job %s has template id %q, expected %q", job.ID, envelope.Job.TemplateID, expected.Job.TemplateID)
	}
	for _, item := range []struct {
		name     string
		actual   any
		expected any
	}{
		{name: "client", actual: envelope.Client, expected: expected.Client},
		{name: "input", actual: envelope.Job.Input, expected: expected.Job.Input},
		{name: "options", actual: envelope.Job.Options, expected: expected.Job.Options},
		{name: "callbacks", actual: envelope.Job.Callbacks, expected: expected.Job.Callbacks},
		{name: "context", actual: envelope.Job.Context, expected: expected.Job.Context},
	} {
		if !jsonSemanticallyEqual(item.actual, item.expected) {
			return fmt.Errorf("leased job %s has mismatched %s", job.ID, item.name)
		}
	}
	return nil
}

func jsonSemanticallyEqual(actual any, expected any) bool {
	if reflect.DeepEqual(actual, expected) {
		return true
	}
	actualJSON, actualErr := json.Marshal(actual)
	expectedJSON, expectedErr := json.Marshal(expected)
	return actualErr == nil && expectedErr == nil && string(actualJSON) == string(expectedJSON)
}

func sanitizeWorkerError(err error) string {
	if err == nil {
		return "worker execution failed"
	}
	if strings.Contains(strings.ToLower(err.Error()), "timed out") {
		return "worker execution timed out"
	}
	return "worker execution failed"
}

type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(input []byte) (int, error) {
	if b.max <= 0 {
		return len(input), nil
	}
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(input), nil
	}
	if len(input) > remaining {
		b.truncated = true
		_, _ = b.buf.Write(input[:remaining])
		return len(input), nil
	}
	_, _ = b.buf.Write(input)
	return len(input), nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func minimalWorkerEnv() []string {
	allowed := map[string]struct{}{
		"PATH":                         {},
		"PATHEXT":                      {},
		"SYSTEMROOT":                   {},
		"WINDIR":                       {},
		"TEMP":                         {},
		"TMP":                          {},
		"HOME":                         {},
		"USERPROFILE":                  {},
		"UBAG_WORKER_SINGLE_USER_EDGE": {},
	}
	env := []string{}
	for _, item := range os.Environ() {
		key, _, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if _, ok := allowed[strings.ToUpper(key)]; ok {
			env = append(env, item)
		}
	}
	return env
}
