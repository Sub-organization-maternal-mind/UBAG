package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/alerts"
	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/plugins"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// manualActionEventType is the worker event that signals a human must solve a
// CAPTCHA, manual login, or verification challenge in the live browser session.
const manualActionEventType = "session.manual_action_required"

// concurrencyChangeEventType is the worker event that reports an AIMD
// tab-ceiling change for a provider/target + identity pair. The worker owns the
// live AIMD controller; the gateway only records the latest reported ceiling
// into its read-only ConcurrencyRegistry so /v1/concurrency reflects live state.
const concurrencyChangeEventType = "concurrency.cap_changed"

// topologyReportEventType is the worker event that reports a live
// browser→context→tab topology snapshot for a job. The worker owns the live
// Fleet; the gateway projects the snapshot into an in-memory topology store
// (when configured) so /v1/browser/* reflects live state for the default
// embedded deployment. SQLite/Postgres topology stores are written by the
// worker out-of-band and ignore this event.
const topologyReportEventType = "browser.topology_reported"

// newChatEventType / configuredEventType are informational worker events emitted
// by the live engine before a prompt is submitted: the worker started a fresh
// conversation and enforced the provider's model/option settings (e.g. DeepSeek
// Expert + DeepThink, Gemini 3.5 Flash + Extended thinking). They are NOT
// job-lifecycle transitions, so — like the orchestration telemetry above — they
// are logged for audit and skipped so their type never poisons the job.
const newChatEventType = "session.new_chat"
const configuredEventType = "session.configured"

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
	Alerts           *alerts.Manager
	Concurrency      *topology.ConcurrencyRegistry
	Topology         topology.TopologyIngestor
	PollInterval     time.Duration
	Plugins          *plugins.Host // optional; nil disables post-job hook
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
	// Artifacts lets the runner materialize a job's audio artifact to a local
	// temp file (audio_local_path) for the worker to attach. Optional; when nil,
	// audio materialization is skipped and text jobs are entirely unaffected.
	Artifacts artifacts.ArtifactStore
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
		c.runPostJobHook(ctx, job)
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
		c.runPostJobHook(ctx, assignedJob)
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
		slog.Error("worker execution error", "job_id", envelope.JobID, "error", err)
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
		// concurrency.cap_changed is orchestration telemetry, not a job-lifecycle
		// event: record the reported ceiling and skip job-event application so the
		// unknown type never poisons the job.
		if normalized.Type == concurrencyChangeEventType {
			c.recordConcurrencyChange(job, normalized)
			continue
		}
		// browser.topology_reported is orchestration telemetry: project the
		// snapshot into the in-memory topology store (when configured) and skip
		// job-event application so the unknown type never poisons the job.
		if normalized.Type == topologyReportEventType {
			c.recordTopologyReport(job, normalized)
			continue
		}
		// session.new_chat / session.configured are informational pre-submit events
		// (fresh conversation + model/option enforcement), not lifecycle
		// transitions: log for audit and skip application so the type never poisons
		// the job (mirrors the orchestration-telemetry handling above).
		if normalized.Type == newChatEventType || normalized.Type == configuredEventType {
			slog.Info("worker session event",
				"job_id", normalized.JobID, "event_type", normalized.Type)
			continue
		}
		if _, found, err := c.Jobs.ApplyWorkerEvent(ctx, normalized); err != nil {
			slog.Error("ApplyWorkerEvent failed", "job_id", normalized.JobID, "event_type", normalized.Type, "error", err)
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
		c.raiseManualActionAlert(ctx, job, normalized)
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
		c.releaseConcurrencyToken(finalJob)
		c.runPostJobHook(ctx, finalJob)
		if err := c.notifyTerminalJob(ctx, lease, finalJob); err != nil {
			return true, err
		}
		return true, lease.Cancel(ctx)
	}
	if finalJob.Status == jobstore.StatusCompleted || finalJob.Status == jobstore.StatusCompletedWithWarnings {
		c.releaseConcurrencyToken(finalJob)
		c.runPostJobHook(ctx, finalJob)
		if err := c.notifyTerminalJob(ctx, lease, finalJob); err != nil {
			return true, err
		}
		return true, lease.Complete(ctx)
	}
	if jobstore.TerminalStatus(finalJob.Status) {
		c.releaseConcurrencyToken(finalJob)
		c.runPostJobHook(ctx, finalJob)
		if err := c.notifyTerminalJob(ctx, lease, finalJob); err != nil {
			return true, err
		}
		return true, lease.Fail(ctx)
	}

	if applyErr := c.applyFailure(ctx, lease, envelope, fmt.Errorf("worker did not emit a terminal event")); applyErr != nil {
		_ = lease.Retry(ctx)
		return true, applyErr
	}
	// runPostJobHook is called inside notifyCurrentTerminalJob before notification.
	if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
		return true, notifyErr
	}
	return true, lease.Fail(ctx)
}

// releaseConcurrencyToken releases the in-flight token for a job when it
// reaches a terminal state. It is nil-safe and a no-op when Concurrency is not
// configured.
func (c *WorkerConsumer) releaseConcurrencyToken(job jobstore.Job) {
	if c == nil || c.Concurrency == nil {
		return
	}
	c.Concurrency.Release(job.TenantID, job.Target, job.AppID)
}

// raiseManualActionAlert raises a human-in-the-loop alert when a worker reports
// that a job needs a manual human action (CAPTCHA, manual login, or
// verification challenge). It is best-effort and nil-safe: a nil alert manager,
// a non-matching event, or a raise failure never interrupts ingestion.
func (c *WorkerConsumer) raiseManualActionAlert(ctx context.Context, job jobstore.Job, event jobstore.WorkerEvent) {
	if c == nil || c.Alerts == nil || event.Type != manualActionEventType {
		return
	}
	data := event.Data
	alert := alerts.Alert{
		TenantID:   job.TenantID,
		AppID:      job.AppID,
		JobID:      job.ID,
		SessionID:  stringFromEventData(data, "session_id"),
		TargetID:   firstNonEmpty(stringFromEventData(data, "target"), job.Target),
		Kind:       manualActionKind(stringFromEventData(data, "reason")),
		Message:    stringFromEventData(data, "message"),
		Attributes: manualActionAttributes(data),
	}
	if _, err := c.Alerts.RaiseManualAction(ctx, alert); err != nil {
		fmt.Fprintf(os.Stderr, "alerts: raise manual action for job %s failed: %v\n", job.ID, err)
	}
}

// recordConcurrencyChange pushes an AIMD cap-change reported by the worker into
// the gateway's read-only ConcurrencyRegistry so /v1/concurrency reflects live,
// worker-reported lane ceilings. It is best-effort and nil-safe: a nil registry,
// a non-matching event, or a missing target never interrupts ingestion. The
// gateway never computes AIMD state; it only records what the worker reports.
func (c *WorkerConsumer) recordConcurrencyChange(job jobstore.Job, event jobstore.WorkerEvent) {
	if c == nil || c.Concurrency == nil || event.Type != concurrencyChangeEventType {
		return
	}
	data := event.Data
	target := firstNonEmpty(stringFromEventData(data, "target"), job.Target)
	if target == "" {
		return
	}
	view := topology.ConcurrencyView{
		Target:           target,
		IdentityRef:      stringFromEventData(data, "identity_ref"),
		CurrentCap:       intFromEventData(data, "current_cap"),
		Min:              intFromEventData(data, "min"),
		Max:              intFromEventData(data, "max"),
		InFlight:         intFromEventData(data, "in_flight"),
		LastChangeReason: stringFromEventData(data, "reason"),
		LastChangeAt:     event.CreatedAt,
	}
	c.Concurrency.Report(job.TenantID, view)
}

// recordTopologyReport projects a worker-reported browser→context→tab snapshot
// into the in-memory topology store so /v1/browser/* reflects live state for the
// default embedded deployment. It is best-effort and nil-safe: a nil ingestor or
// malformed payload never interrupts ingestion. Tenant identity is always taken
// from the job (never the worker payload) to enforce tenant isolation, and
// storage-state material is never present (only the HasStorageState boolean).
func (c *WorkerConsumer) recordTopologyReport(job jobstore.Job, event jobstore.WorkerEvent) {
	if c == nil || c.Topology == nil || event.Type != topologyReportEventType {
		return
	}
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	var instances []topology.BrowserInstance
	if decodeEventList(event.Data, "instances", &instances) {
		for i := range instances {
			instances[i].TenantID = job.TenantID
			instances[i].CreatedAt = createdAt
			c.Topology.AddInstance(instances[i])
		}
	}

	var contexts []topology.ProviderContext
	if decodeEventList(event.Data, "contexts", &contexts) {
		for i := range contexts {
			contexts[i].TenantID = job.TenantID
			contexts[i].CreatedAt = createdAt
			// Defensive: telemetry never carries storage-state material.
			contexts[i].HasStorageState = false
			c.Topology.AddContext(contexts[i])
		}
	}

	var tabs []topology.BrowserTab
	if decodeEventList(event.Data, "tabs", &tabs) {
		for i := range tabs {
			tabs[i].CreatedAt = createdAt
			c.Topology.AddTab(tabs[i])
		}
	}
}

// decodeEventList re-marshals a nested worker-event list field and unmarshals it
// into the typed topology slice (whose JSON tags match the worker payload keys).
// Returns false on any error so a malformed field is silently skipped.
func decodeEventList(data map[string]any, key string, out any) bool {
	if data == nil {
		return false
	}
	raw, ok := data[key]
	if !ok {
		return false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return false
	}
	return true
}

func manualActionKind(reason string) string {
	reason = strings.ToLower(reason)
	switch {
	case strings.Contains(reason, "captcha"):
		return alerts.KindCaptcha
	case strings.Contains(reason, "login"):
		return alerts.KindManualLogin
	case strings.Contains(reason, "verification"), strings.Contains(reason, "2fa"), strings.Contains(reason, "challenge"):
		return alerts.KindVerification
	case reason == "":
		return alerts.KindManualLogin
	default:
		return alerts.KindOther
	}
}

func manualActionAttributes(data map[string]any) map[string]any {
	keys := []string{"adapter", "reason", "novnc_url", "account_binding_id", "consent_ref", "automation_scope"}
	attrs := make(map[string]any, len(keys))
	for _, key := range keys {
		if value := stringFromEventData(data, key); value != "" {
			attrs[key] = value
		}
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}

func stringFromEventData(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	if value, ok := data[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// intFromEventData extracts an integer from worker event data. JSON numbers
// decode as float64, so both float64 and int forms are accepted. Non-numeric or
// missing values yield 0.
func intFromEventData(data map[string]any, key string) int {
	if data == nil {
		return 0
	}
	switch value := data[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
	job, found, err := c.Jobs.Get(ctx, lease.JobID())
	if err != nil {
		_ = lease.Retry(ctx)
		return err
	}
	if !found {
		_ = lease.Poison(ctx, "terminal notification referenced missing job")
		return fmt.Errorf("terminal notification referenced missing job %s", lease.JobID())
	}
	c.runPostJobHook(ctx, job)
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

// runPostJobHook fires the job.post plugin hook if Plugins is configured.
// It is best-effort: errors are silently ignored so terminal processing always completes.
func (c *WorkerConsumer) runPostJobHook(ctx context.Context, job jobstore.Job) {
	if c.Plugins == nil {
		return
	}
	hookPayload, _ := json.Marshal(map[string]any{
		"job_id": job.ID,
		"status": string(job.Status),
		"target": job.Target,
	})
	_, _ = c.Plugins.RunHooks(ctx, "job.post", hookPayload)
}

// materializeAudioArtifact writes a job's audio artifact (named by
// input.audio_artifact_key) to a local temp file and sets input.audio_local_path
// to its path, returning a cleanup func that removes the file. It is a no-op
// (nil, nil) when the runner has no store, the job carries no audio_artifact_key,
// or the key is blank — so text jobs are completely unaffected.
func (r ProcessWorkerRunner) materializeAudioArtifact(ctx context.Context, envelope *DispatchEnvelope) (func(), error) {
	if r.Artifacts == nil || envelope == nil || envelope.Job.Input == nil {
		return nil, nil
	}
	keyVal, ok := envelope.Job.Input["audio_artifact_key"]
	if !ok {
		return nil, nil
	}
	key, ok := keyVal.(string)
	if !ok {
		return nil, nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}

	rc, _, err := r.Artifacts.GetArtifact(ctx, envelope.JobID, key)
	if err != nil {
		return nil, fmt.Errorf("materialize audio artifact %q: %w", key, err)
	}
	defer func() { _ = rc.Close() }()

	tmp, err := os.CreateTemp("", "ubag-audio-*"+filepath.Ext(key))
	if err != nil {
		return nil, fmt.Errorf("create temp audio file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := io.Copy(tmp, rc); err != nil {
		_ = tmp.Close()
		cleanup()
		return nil, fmt.Errorf("write audio artifact %q: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return nil, fmt.Errorf("close temp audio file: %w", err)
	}

	envelope.Job.Input["audio_local_path"] = tmpPath
	return cleanup, nil
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

	// Materialize a dictation audio artifact (if any) to a local temp file that
	// the worker subprocess can attach. The gateway already holds the bytes in
	// its artifact store, so it writes them locally and injects audio_local_path
	// — the worker never needs gateway credentials. No-op for text jobs.
	cleanupAudio, err := r.materializeAudioArtifact(runCtx, &envelope)
	if err != nil {
		return nil, err
	}
	if cleanupAudio != nil {
		defer cleanupAudio()
	}

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
		slog.Error("worker process failed", "stderr", stderr.buf.String(), "stdout_bytes", stdout.buf.Len(), "error", err)
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
		"UBAG_BROWSER_ENGINE":          {},
		"UBAG_BROWSER_HEADED":          {},
		"UBAG_BROWSER_PROTOCOL":        {},
		"UBAG_NOVNC_BASE_URL":          {},
		"UBAG_REMOTE_BROWSER_ENDPOINT": {},
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
