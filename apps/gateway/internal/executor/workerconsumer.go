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
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/alerts"
	"github.com/ubag/ubag/apps/gateway/internal/antigravity"
	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/attachments"
	"github.com/ubag/ubag/apps/gateway/internal/conversations"
	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	"github.com/ubag/ubag/apps/gateway/internal/jobcore"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/plugins"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	"github.com/ubag/ubag/apps/gateway/internal/workerdaemon"
)

// manualActionEventType is the worker event that signals a human must solve a
// CAPTCHA, manual login, or verification challenge in the live browser session.
// It also carries the live engine's real login state: a manual action means the
// user-owned session is not authenticated (login_required).
const manualActionEventType = "session.manual_action_required"

// sessionAuthenticatedEventType is the worker event emitted once the live engine
// has confirmed (via detect_login_state) that the user-owned browser session is
// authenticated for the job's target. The gateway persists this real state onto
// the served provider-context topology so /v1/browser/contexts reflects reality.
const sessionAuthenticatedEventType = "session.authenticated"

// concurrencyChangeEventType is the worker event that reports an AIMD
// tab-ceiling change for a provider/target + identity pair. The worker owns the
// live AIMD controller; the gateway only records the latest reported ceiling
// into its read-only ConcurrencyRegistry so /v1/concurrency reflects live state.
const concurrencyChangeEventType = "concurrency.cap_changed"

// topologyReportEventType is the worker event that reports a live
// browserÃ¢â€ â€™contextÃ¢â€ â€™tab topology snapshot for a job. The worker owns the live
// Fleet; the gateway projects the snapshot into an in-memory topology store
// (when configured) so /v1/browser/* reflects live state for the default
// embedded deployment. SQLite/Postgres topology stores are written by the
// worker out-of-band and ignore this event.
const topologyReportEventType = "browser.topology_reported"

// newChatEventType / configuredEventType are informational worker events emitted
// by the live engine before a prompt is submitted: the worker started a fresh
// conversation and enforced the provider's model/option settings (e.g. DeepSeek
// Expert + DeepThink, Gemini 3.5 Flash + Extended thinking). They are NOT
// job-lifecycle transitions, so Ã¢â‚¬â€ like the orchestration telemetry above Ã¢â‚¬â€ they
// are logged for audit and skipped so their type never poisons the job.
const newChatEventType = "session.new_chat"
const configuredEventType = "session.configured"

// fileAttachedEventType reports that the worker attached one or more files to the
// provider composer before submitting the prompt. It is informational telemetry,
// not a job-lifecycle transition, so it is logged and skipped Ã¢â‚¬â€ its unknown type
// must never poison the job (mirrors the session.* handling above).
const fileAttachedEventType = "file.attached"

// conversation.thread_bound / .thread_rebound / .thread_broken are
// conversation-affinity telemetry emitted by the live engine after it binds,
// rebinds, or loses a provider chat thread for a job that carries a conversation
// key. Like the orchestration telemetry above they are NOT job-lifecycle
// transitions: the gateway projects them into its conversations store (when
// configured) and skips job-event application so the unknown type never poisons
// the job. thread_bound/thread_rebound upsert the binding; thread_broken marks
// it broken so a reused key fails fast instead of resuming a dead chat.
const conversationThreadBoundEventType = "conversation.thread_bound"
const conversationThreadReboundEventType = "conversation.thread_rebound"
const conversationThreadBrokenEventType = "conversation.thread_broken"

// conversationThreadRefField is the ONLY field the gateway reads from a
// conversation.* event payload: the provider chat URL. Every identity field
// (tenant, app, target, conversation key) is forced from the trusted job record,
// never the worker payload Ã¢â‚¬â€ the redaction boundary that keeps a buggy or
// compromised worker from binding another tenant's conversation or persisting
// non-URL material (cookies, storage state, noVNC URLs). It mirrors the
// thread_ref field the gateway sends down in the dispatch envelope.
const conversationThreadRefField = "thread_ref"

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
	// Conversations projects worker-reported conversation.* events into the
	// conversation-affinity store so a reused conversation key resumes the same
	// provider chat thread. Optional; nil disables conversation projection (the
	// events are still intercepted so they never poison the job).
	Conversations *conversations.Manager
	// LoginState persists the live engine's real detect_login_state result onto
	// the SERVED topology store (Postgres/SQLite/in-memory), keyed by tenant +
	// target. Optional; nil disables login-state projection.
	LoginState   topology.LoginStateWriter
	PollInterval time.Duration
	// IdlePollMax, when greater than PollInterval, lets an idle lease loop back
	// off its fallback poll (doubling per consecutive empty poll) up to this
	// cap, and snap back to PollInterval after any work or error. The enqueue
	// wake channel still pre-empts the wait, so same-process enqueues are not
	// delayed; the cap only bounds pickup latency for enqueues the wake channel
	// cannot see. Zero keeps the fixed PollInterval.
	IdlePollMax time.Duration
	// PoolSize is the number of parallel lease-process workers in Run.
	// 0/negative means 1 (legacy serial behavior). Clamped to 32 in workerCount.
	// Each worker loops RunOnce independently; FileSpool rename-CAS and NATS
	// fetch+ack are safe for concurrent LeaseNext. ProcessWorkerRunner is
	// stateless (one subprocess per job) so mock/per-job jobs truly overlap;
	// DaemonWorkerRunner keeps its mu so warm-daemon jobs stay serial there. A
	// DaemonPool (UBAG_WORKER_POOL_SIZE > 1) runs up to its Size jobs at once, so
	// PoolSize must be at least that large for the slots to be used (the serve
	// package raises it); jobs it cannot place are Retried after a delay.
	PoolSize int
	Plugins  *plugins.Host // optional; nil disables post-job hook
	Metrics  WorkerMetricsRecorder

	// ExecLeases, when set, makes execution of a job exclusive across every
	// consumer sharing the backend: before running, the consumer takes an
	// expiring per-job lease (renewed by a heartbeat while the job runs). A
	// second delivery of the same job — queue redelivery, a retried enqueue —
	// that finds the lease held is NOT run: it is nacked, so the provider is
	// never submitted to twice concurrently. A crashed holder's lease simply
	// expires and the redelivery proceeds. If the lease is lost mid-run (the
	// holder was presumed dead) the local run is cancelled. Nil keeps the
	// queue's own lease as the only guard (single-process deployments).
	ExecLeases topology.TokenBackend
	// HeartbeatInterval paces lease/queue heartbeats (default 10s).
	HeartbeatInterval time.Duration

	// VoiceLanes, when set (a gateway with voice sessions), makes a job that
	// drives the shared browser wait while a live voice session holds it: the
	// job registers on the browser lane in Concurrency, then asks the probe, and
	// a held lane sends it back to the queue after a delay (voicelane.go). Nil
	// (voice off, or UBAG_VOICE_LANE_EXCLUSION=0) never looks.
	VoiceLanes VoiceLaneProbe
	// VoiceLaneRetryDelay is how long a job held back by a voice session waits
	// before its lease goes back to the queue. Zero is 2s (the pool overload delay).
	VoiceLaneRetryDelay time.Duration

	// Remote, when set (UBAG_HELPER_DISPATCH, default off), offers each leased job
	// to the helper plane before it runs locally: Place decides (a picker, after
	// the static eligibility rules) and a placed job runs as an attempt on a
	// Helper Node (remoterunner.go). Every other job, and every job when the
	// picker has nothing, takes the unchanged local path. Nil is today's gateway.
	Remote *RemoteWorkerRunner
	// Cancels, when set, is the in-process cancel path for remote attempts: the
	// cancel API hints it (NewCancelNotifier) and the supervisor of a remote run
	// stops the helper at once instead of waiting for the store poll. Nil keeps
	// the poll as the only path.
	Cancels *CancelRegistry

	inflight atomic.Int64
}

// LeaseHeartbeater is the optional WorkerLease capability for queue leases whose
// redelivery timer must be extended while a long-running job executes (NATS
// InProgress; the file spool's mtime renewal under UBAG_EXECUTOR_LEASE_TTL_MS).
// It is the lease-renewal seam: leases without it are simply not renewed. A
// renewal that returns ErrLeaseLost tells the consumer the queue has already
// handed the job to someone else, so the local run is cancelled.
type LeaseHeartbeater interface {
	Heartbeat(ctx context.Context) error
}

// ErrLeaseLost is returned by a renewal when the queue lease no longer belongs
// to the holder (expired and reclaimed).
var ErrLeaseLost = errors.New("queue lease lost")

// renewReason maps a failed renewal to the bounded metric reason.
func renewReason(lost bool) string {
	if lost {
		return helpermetrics.ReasonLost
	}
	return helpermetrics.ReasonError
}

const (
	execLeaseTTL             = 90 * time.Second
	defaultHeartbeatInterval = 10 * time.Second
)

func (c *WorkerConsumer) heartbeatInterval() time.Duration {
	if c.HeartbeatInterval > 0 {
		return c.HeartbeatInterval
	}
	return defaultHeartbeatInterval
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

// changeNotifier is the optional WorkerQueue capability that surfaces a wake
// channel fed by the enqueue path. The lease loop selects on it so a job is
// picked up within moments of enqueue instead of after the next poll tick;
// the polling ticker stays as the correctness fallback.
type changeNotifier interface {
	EnqueueNotify() <-chan struct{}
}

type WorkerMetricsRecorder interface {
	ObserveQueueWait(duration time.Duration)
	ObserveWorkerRun(target, outcome string, duration time.Duration)
	ObserveWorkerResultIngestion(target, outcome, errorClass string, events int, duration time.Duration)
	ObserveJobEndToEnd(jobID string, status jobstore.Status, duration time.Duration)
}

type WorkerRunFunc func(ctx context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error)

func (f WorkerRunFunc) RunWorker(ctx context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
	return f(ctx, envelope)
}

type ProcessWorkerRunner struct {
	Python           string
	Script           string
	MaxRuntime       time.Duration
	AntigravityStore *antigravity.Store
	Jobs             jobstore.Store
	// Artifacts lets the runner materialize a job's declared attachments to local
	// temp files (attachment_local_paths, plus audio_local_path for the single
	// audio alias) for the worker to attach. Optional; when nil, materialization
	// is skipped and text jobs are entirely unaffected.
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
	n := c.workerCount()
	if n <= 1 {
		return c.runSerial(ctx)
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.runSerial(child); err != nil && err != context.Canceled && err != child.Err() {
				select {
				case errCh <- err:
				default:
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	return ctx.Err()
}

// workerCount normalizes PoolSize: unset/non-positive keeps the legacy
// single worker; the ceiling bounds FileSpool ReadDir fan-out per poll.
func (c *WorkerConsumer) workerCount() int {
	if c == nil || c.PoolSize <= 0 {
		return 1
	}
	if c.PoolSize > 32 {
		return 32
	}
	return c.PoolSize
}

// Inflight reports jobs currently leased-and-executing across Run workers.
func (c *WorkerConsumer) Inflight() int64 {
	if c == nil {
		return 0
	}
	return c.inflight.Load()
}

// idlePollWait returns the fallback poll wait after streak consecutive empty
// polls: base doubled per empty poll, capped at max. max <= base keeps the
// fixed base interval.
func idlePollWait(base, max time.Duration, streak int) time.Duration {
	if max <= base || streak <= 0 {
		return base
	}
	wait := base << min(streak, 16)
	if wait <= 0 || wait > max {
		return max
	}
	return wait
}

func (c *WorkerConsumer) runSerial(ctx context.Context) error {
	pollInterval := c.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultWorkerPollInterval
	}
	consecutiveErrors := 0
	idleStreak := 0
	for {
		processed, err := c.RunOnce(ctx)
		if err != nil {
			// A transient store error (e.g. a dropped database connection)
			// must not kill the consumer: the queue serves jobs 24/7 and a
			// dead consumer silently freezes every queued job until the
			// gateway is manually restarted. RunOnce has already requeued or
			// poisoned the lease on each of its error paths, so back off and
			// keep polling. Context cancellation still exits.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			consecutiveErrors++
			backoff := pollInterval * time.Duration(1<<min(consecutiveErrors, 5))
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			slog.Warn("worker consumer run failed; backing off",
				"error", err, "consecutive_errors", consecutiveErrors, "backoff", backoff)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		consecutiveErrors = 0
		if processed {
			idleStreak = 0
			continue
		}
		timer := time.NewTimer(idlePollWait(pollInterval, c.IdlePollMax, idleStreak))
		idleStreak++
		// Wake immediately when the queue signals a fresh enqueue instead of
		// waiting out the full poll interval. The ticker remains the
		// correctness fallback: the notification is a hint (cap-1, lossy), so
		// an empty wakeup just re-polls and waits again. A nil channel (queue
		// without the capability) blocks forever, degrading to pure polling.
		var wake <-chan struct{}
		if queue, err := c.workerQueue(); err == nil {
			if notifier, ok := queue.(changeNotifier); ok {
				wake = notifier.EnqueueNotify()
			}
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		case <-wake:
			timer.Stop()
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
	c.inflight.Add(1)
	defer c.inflight.Add(-1)
	leasedAt := time.Now()

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
	queueEnteredAt := job.UpdatedAt
	if job.Status == jobstore.StatusScheduled && job.NotBefore != nil &&
		job.NotBefore.After(queueEnteredAt) {
		queueEnteredAt = *job.NotBefore
	}
	if (job.Status == jobstore.StatusQueued || job.Status == jobstore.StatusScheduled) &&
		!queueEnteredAt.IsZero() && !leasedAt.Before(queueEnteredAt) && c.Metrics != nil {
		c.Metrics.ObserveQueueWait(leasedAt.Sub(queueEnteredAt))
	}
	envelope := EnvelopeFromJobWithConversation(ctx, job, c.Conversations)
	// UBAG_WORKER_STREAM_INGEST applies a stream's events before the job ends, so
	// a redelivered job would re-emit legacy job:seq ids that collide with the
	// abandoned attempt's already-applied events and be dropped by the store.
	// Streaming therefore implies attempt-scoped ids.
	streaming := c.streamsLive(envelope)
	if attemptEventIDsEnabled() || streaming {
		envelope.Attempt = &DispatchAttempt{ID: lease.LeaseID()}
	}
	if jobstore.TerminalStatus(job.Status) {
		return c.finishTerminalLeasedJob(ctx, lease, job)
	}
	var execToken string
	releaseExecLease := func() {}
	if c.ExecLeases != nil {
		token, held, leaseErr := c.ExecLeases.AcquireToken(ctx,
			[]topology.Lane{{Key: "exec:" + job.ID, Cap: 1}}, execLeaseTTL, time.Now().UTC())
		if leaseErr != nil {
			_ = lease.Retry(ctx)
			return true, fmt.Errorf("acquire execution lease for job %s: %w", job.ID, leaseErr)
		}
		if !held {
			// Another consumer is running (or recently ran) this job: a
			// duplicate delivery must not reach the provider.
			_ = lease.Retry(ctx)
			return true, nil
		}
		execToken = token
		// Idempotent: the overload path releases early (before the delayed
		// Retry), and the deferred call then does nothing.
		var releaseOnce sync.Once
		releaseExecLease = func() {
			releaseOnce.Do(func() {
				releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = c.ExecLeases.ReleaseToken(releaseCtx, execToken)
			})
		}
		defer releaseExecLease()
	}
	// Helper placement (UBAG_HELPER_DISPATCH): lease-then-place, before the job is
	// assigned. A nil placement is the local path below; an error holds the job
	// back (it never started) and retries the lease after a delay.
	placed, placeErr := c.Remote.Place(ctx, envelope)
	if placeErr != nil {
		releaseExecLease()
		return c.retryAfterDelay(ctx, lease, placeErr)
	}
	defer placed.Release()
	remote := placed != nil
	// A browser job and a live voice session exclude each other on the browser
	// (voicelane.go). Checked before the job is assigned: a held-back job has not
	// started, so it stays queued and goes back to the queue after a delay. A job
	// placed on a helper drives the helper's browser, not this one.
	var laneHold *topology.LaneHold
	if !remote {
		var laneErr error
		if laneHold, laneErr = c.enterBrowserLane(ctx, envelope); laneErr != nil {
			releaseExecLease()
			return c.retryAfterDelay(ctx, lease, laneErr)
		}
	}
	defer laneHold.Release()
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
		return c.finishTerminalLeasedJob(ctx, lease, assignedJob)
	}

	// Streaming: the sink applies non-terminal events while the worker runs and
	// holds the single terminal; the run then returns just that terminal, which
	// the ingestion below applies exactly like the batch path's last event.
	var ingest *streamIngest
	if streaming && !remote {
		ingest = &streamIngest{c: c, job: job, envelope: envelope}
	}
	workerStarted := time.Now()
	var events []jobstore.WorkerEvent
	if remote {
		// The helper's events are committed through the fenced ingest as they
		// arrive; the run itself returns no events, only whether it reached an
		// end the store holds (below).
		_, err = c.superviseRun(ctx, lease, execToken, laneHold, envelope.JobID, true, func(runCtx context.Context) ([]jobstore.WorkerEvent, error) {
			return nil, c.Remote.Run(runCtx, envelope, placed)
		})
	} else {
		events, err = c.runWorkerWithCancellation(ctx, lease, execToken, laneHold, envelope, ingest)
	}
	workerDuration := time.Since(workerStarted)
	var heldBack *HelperRetryError
	if err != nil && ctx.Err() == nil && (errors.Is(err, ErrPoolOverloaded) || errors.As(err, &heldBack)) {
		// Lease-then-place (ADR-0011): the job was leased but no worker slot (or
		// no helper) could take it. It never ran, so it is neither failed nor
		// completed.
		releaseExecLease()
		return c.retryAfterDelay(ctx, lease, err)
	}
	if err != nil {
		outcome := "failure"
		if errors.Is(err, context.Canceled) {
			outcome = "cancelled"
		}
		c.observeWorkerRun(job.Target, outcome, workerDuration)
		slog.Error("worker execution error", "job_id", envelope.JobID, "error", err)
		var ingestErr *streamIngestError
		if errors.As(err, &ingestErr) {
			// The stream was abandoned by the ingestion itself (same classes the
			// batch path reports after the run).
			c.observeIngestion(job.Target, "failure", ingestErr.class, ingestErr.events, ingest.spent)
			if ingestErr.missingJob {
				_ = lease.Poison(ctx, "worker event referenced missing job")
				return true, fmt.Errorf("worker event referenced missing job %s", job.ID)
			}
		}
		// Post-submit failure (UBAG_WORKER_STRICT_SUBMIT): the provider may hold
		// the turn, so never lease.Retry (shutdown and lease loss included).
		// Terminal writes use a context that survives a cancelled parent.
		ambiguous := errors.Is(err, ErrAmbiguous)
		opCtx := ctx
		if ambiguous {
			var opCancel context.CancelFunc
			opCtx, opCancel = detachedOpContext(ctx)
			defer opCancel()
		}
		if ctx.Err() != nil && !ambiguous {
			_ = lease.Retry(ctx)
			return true, err
		}
		if errors.Is(err, context.Canceled) {
			finalJob, found, finalErr := c.Jobs.Get(context.Background(), job.ID)
			if finalErr == nil && found && finalJob.Status == jobstore.StatusCanceled {
				c.observeTerminalJob(finalJob)
				return true, lease.Cancel(opCtx)
			}
			if finalErr == nil && found && jobstore.TerminalStatus(finalJob.Status) {
				// Ended under the running worker (stale-job reaper timeout,
				// terminal write elsewhere): the watcher stopped the run; close
				// the lease instead of re-queueing a finished job.
				return c.finishTerminalLeasedJob(opCtx, lease, finalJob)
			}
			if !ambiguous {
				_ = lease.Retry(ctx)
				if finalErr != nil {
					return true, finalErr
				}
				return true, err
			}
		}
		if applyErr := c.applyFailure(opCtx, lease, envelope, err); applyErr != nil {
			if ambiguous {
				// No replay even when the failure cannot be recorded; the
				// stale-job reaper settles the job. (Ledger-backed in P3.8.)
				_ = lease.Fail(opCtx)
			} else {
				_ = lease.Retry(ctx)
			}
			return true, applyErr
		}
		if notifyErr := c.notifyCurrentTerminalJob(opCtx, lease); notifyErr != nil {
			return true, notifyErr
		}
		return true, lease.Fail(opCtx)
	}
	if remote {
		return c.finishRemoteRun(ctx, lease, job, workerDuration)
	}
	ingestionStarted := time.Now()
	eventCount := len(events)
	if ingest != nil {
		// Streamed: the events were applied during the run, so count them all and
		// credit the time already spent applying them.
		eventCount = ingest.events
		ingestionStarted = ingestionStarted.Add(-ingest.spent)
	}
	if len(events) == 0 {
		c.observeWorkerRun(job.Target, "failure", workerDuration)
		c.observeIngestion(job.Target, "failure", "empty_result", 0, time.Since(ingestionStarted))
		if applyErr := c.applyFailure(ctx, lease, envelope, fmt.Errorf("worker emitted no events")); applyErr != nil {
			_ = lease.Retry(ctx)
			return true, applyErr
		}
		if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
			return true, notifyErr
		}
		return true, lease.Fail(ctx)
	}

	normalizedEvents := make([]jobstore.WorkerEvent, len(events))
	terminalEvents := 0
	events = dropStaleAttemptEvents(envelope, events)
	// The submission boundary is judged on everything this attempt produced: a
	// streamed run applied its earlier events during the run, so carry the
	// sink's prompt_submitted sighting into the evidence.
	evidence := events
	if ingest != nil && ingest.submitted {
		evidence = append([]jobstore.WorkerEvent{{Type: promptSubmittedEventType}}, events...)
	}
	for index, event := range events {
		normalized, err := normalizeWorkerEvent(envelope, event)
		if err != nil {
			c.observeWorkerRun(job.Target, "failure", workerDuration)
			c.observeIngestion(job.Target, "failure", "invalid_event", index+1, time.Since(ingestionStarted))
			if applyErr := c.applyFailure(ctx, lease, envelope, ambiguousIfSubmitted(err, evidence)); applyErr != nil {
				_ = lease.Retry(ctx)
				return true, applyErr
			}
			if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
				return true, notifyErr
			}
			return true, lease.Fail(ctx)
		}
		normalizedEvents[index] = normalized
		if terminalWorkerEventType(normalized.Type) {
			terminalEvents++
		}
	}
	if terminalEvents != 1 {
		c.observeWorkerRun(job.Target, "failure", workerDuration)
		errorClass := "invalid_event"
		if terminalEvents == 0 {
			errorClass = "missing_terminal"
		}
		c.observeIngestion(job.Target, "failure", errorClass, eventCount, time.Since(ingestionStarted))
		if applyErr := c.applyFailure(
			ctx,
			lease,
			envelope,
			ambiguousIfSubmitted(fmt.Errorf("worker emitted %d terminal events; expected exactly one", terminalEvents), evidence),
		); applyErr != nil {
			_ = lease.Retry(ctx)
			return true, applyErr
		}
		if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
			return true, notifyErr
		}
		return true, lease.Fail(ctx)
	}

	for index, normalized := range normalizedEvents {
		if c.handleTelemetryEvent(ctx, job, normalized) {
			continue
		}
		if _, found, err := c.Jobs.ApplyWorkerEvent(ctx, normalized); err != nil {
			c.observeWorkerRun(job.Target, "failure", workerDuration)
			c.observeIngestion(job.Target, "failure", "store", index+1, time.Since(ingestionStarted))
			slog.Error("ApplyWorkerEvent failed", "job_id", normalized.JobID, "event_type", normalized.Type, "error", err)
			if applyErr := c.applyFailure(ctx, lease, envelope, ambiguousIfSubmitted(err, evidence)); applyErr != nil {
				_ = lease.Retry(ctx)
				return true, applyErr
			}
			if notifyErr := c.notifyCurrentTerminalJob(ctx, lease); notifyErr != nil {
				return true, notifyErr
			}
			return true, lease.Fail(ctx)
		} else if !found {
			c.observeWorkerRun(job.Target, "failure", workerDuration)
			c.observeIngestion(job.Target, "failure", "missing_job", index+1, time.Since(ingestionStarted))
			_ = lease.Poison(ctx, "worker event referenced missing job")
			return true, fmt.Errorf("worker event referenced missing job %s", normalized.JobID)
		}
		c.afterEventApplied(ctx, job, normalized)
	}

	finalJob, found, err := c.Jobs.Get(ctx, lease.JobID())
	if err != nil {
		c.observeWorkerRun(job.Target, "failure", workerDuration)
		c.observeIngestion(job.Target, "failure", "store", eventCount, time.Since(ingestionStarted))
		_ = lease.Retry(ctx)
		return true, err
	}
	if !found {
		c.observeWorkerRun(job.Target, "failure", workerDuration)
		c.observeIngestion(job.Target, "failure", "missing_job", eventCount, time.Since(ingestionStarted))
		_ = lease.Poison(ctx, "job disappeared during worker ingestion")
		return true, fmt.Errorf("job %s disappeared during worker ingestion", lease.JobID())
	}
	if jobstore.TerminalStatus(finalJob.Status) {
		c.observeWorkerRun(job.Target, workerMetricOutcome(finalJob.Status), workerDuration)
		c.observeIngestion(job.Target, "success", "none", eventCount, time.Since(ingestionStarted))
		c.observeStageTimings(job.Target, events)
	}
	if finalJob.Status == jobstore.StatusCanceled {
		return c.finishTerminalIngestedJob(ctx, lease, finalJob, lease.Cancel)
	}
	if finalJob.Status == jobstore.StatusCompleted || finalJob.Status == jobstore.StatusCompletedWithWarnings {
		return c.finishTerminalIngestedJob(ctx, lease, finalJob, lease.Complete)
	}
	if jobstore.TerminalStatus(finalJob.Status) {
		return c.finishTerminalIngestedJob(ctx, lease, finalJob, lease.Fail)
	}

	c.observeWorkerRun(job.Target, "failure", workerDuration)
	c.observeIngestion(job.Target, "failure", "missing_terminal", eventCount, time.Since(ingestionStarted))
	if applyErr := c.applyFailure(ctx, lease, envelope, ambiguousIfSubmitted(fmt.Errorf("worker did not reach a terminal status"), evidence)); applyErr != nil {
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
// configured. Release is keyed by job ID and idempotent, so every terminal path
// may call it without risk of double-counting the shared lane.
func (c *WorkerConsumer) releaseConcurrencyToken(job jobstore.Job) {
	if c == nil || c.Concurrency == nil {
		return
	}
	c.Concurrency.ReleaseForJob(job.ID)
}

func (c *WorkerConsumer) observeIngestion(target, outcome, errorClass string, events int, duration time.Duration) {
	if c != nil && c.Metrics != nil {
		c.Metrics.ObserveWorkerResultIngestion(target, outcome, errorClass, events, duration)
	}
}

func (c *WorkerConsumer) observeWorkerRun(target, outcome string, duration time.Duration) {
	if c != nil && c.Metrics != nil {
		c.Metrics.ObserveWorkerRun(target, outcome, duration)
	}
}

func terminalWorkerEventType(eventType string) bool {
	switch eventType {
	case "completed", "completed_with_warnings",
		"failed", "failed_retryable", "failed_terminal",
		"dead_letter", "cancelled", "canceled",
		"timed_out", "timeout", "blocked":
		return true
	default:
		return false
	}
}

// handleTelemetryEvent consumes the worker's orchestration and session telemetry
// and reports whether event was one. None of these are job-lifecycle events, so
// they are recorded (or logged) and never reach job-event application, where
// their unknown types would poison the job. Shared by the batch ingestion and
// the streaming sink so both treat telemetry identically.
func (c *WorkerConsumer) handleTelemetryEvent(ctx context.Context, job jobstore.Job, normalized jobstore.WorkerEvent) bool {
	switch normalized.Type {
	case concurrencyChangeEventType:
		// Record the reported AIMD ceiling.
		c.recordConcurrencyChange(job, normalized)
	case topologyReportEventType:
		// Project the snapshot into the in-memory topology store (when configured).
		c.recordTopologyReport(job, normalized)
	case conversationThreadBoundEventType, conversationThreadReboundEventType, conversationThreadBrokenEventType:
		c.recordConversationEvent(ctx, job, normalized)
	case newChatEventType, configuredEventType, fileAttachedEventType:
		// Informational pre-submit events (fresh conversation, model/option
		// enforcement, attachments): log for audit only.
		slog.Info("worker session event",
			"job_id", normalized.JobID, "event_type", normalized.Type)
	default:
		return false
	}
	return true
}

// afterEventApplied runs the best-effort projections that follow a job event
// that was applied to the store.
func (c *WorkerConsumer) afterEventApplied(ctx context.Context, job jobstore.Job, normalized jobstore.WorkerEvent) {
	c.raiseManualActionAlert(ctx, job, normalized)
	c.recordLoginState(ctx, job, normalized)
	if normalized.Type == "completed" {
		c.recordConversationEvent(ctx, job, normalized)
	}
}

func workerMetricOutcome(status jobstore.Status) string {
	switch status {
	case jobstore.StatusCompleted, jobstore.StatusCompletedWithWarnings:
		return "success"
	case jobstore.StatusCanceled:
		return "cancelled"
	default:
		return "failure"
	}
}

func (c *WorkerConsumer) observeTerminalJob(job jobstore.Job) {
	if c == nil || c.Metrics == nil || !jobstore.TerminalStatus(job.Status) ||
		job.CreatedAt.IsZero() || job.UpdatedAt.Before(job.CreatedAt) {
		return
	}
	c.Metrics.ObserveJobEndToEnd(job.ID, job.Status, job.UpdatedAt.Sub(job.CreatedAt))
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
		TargetID:   jobcore.FirstNonEmpty(stringFromEventData(data, "target"), job.Target),
		Kind:       manualActionKind(stringFromEventData(data, "reason")),
		Message:    stringFromEventData(data, "message"),
		Attributes: manualActionAttributes(data),
	}
	if _, err := c.Alerts.RaiseManualAction(ctx, alert); err != nil {
		fmt.Fprintf(os.Stderr, "alerts: raise manual action for job %s failed: %v\n", job.ID, err)
	}
}

// recordLoginState projects the live engine's real login state for a provider
// context into the SERVED topology store so /v1/browser/contexts reflects the
// user-owned session's actual auth state instead of the deploy-time seed. It maps
// session.authenticated Ã¢â€ â€™ "authenticated" and session.manual_action_required Ã¢â€ â€™
// "login_required" (the worker's own detect_login_state vocabulary), keyed by the
// job's tenant + the event's target Ã¢â‚¬â€ the same join consumers use to match a
// context to a target. Events arrive in order, so a job that surfaces a manual
// action and is then completed after the human logs in ends "authenticated".
//
// It is best-effort and nil-safe: a nil writer, a non-login event, a missing
// target, or a write error never interrupts job ingestion. A zero-row update
// (target not registered in this tenant's topology) is a benign no-op.
func (c *WorkerConsumer) recordLoginState(ctx context.Context, job jobstore.Job, event jobstore.WorkerEvent) {
	if c == nil || c.LoginState == nil {
		return
	}
	var loginState string
	switch event.Type {
	case sessionAuthenticatedEventType:
		loginState = "authenticated"
	case manualActionEventType:
		loginState = "login_required"
	default:
		return
	}
	target := jobcore.FirstNonEmpty(stringFromEventData(event.Data, "target"), job.Target)
	if target == "" {
		return
	}
	// last_health_at records when the gateway OBSERVED this login state, so it is
	// stamped with wall-clock now Ã¢â‚¬â€ not event.CreatedAt, which the live worker
	// derives from a deterministic synthetic clock (a fixed early-2026 value).
	if _, err := c.LoginState.UpdateContextLoginState(ctx, job.TenantID, target, loginState, time.Now().UTC()); err != nil {
		slog.Warn("topology login-state projection failed",
			"job_id", job.ID, "target", target, "login_state", loginState, "error", err)
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
	target := jobcore.FirstNonEmpty(stringFromEventData(data, "target"), job.Target)
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

// recordTopologyReport projects a worker-reported browserÃ¢â€ â€™contextÃ¢â€ â€™tab snapshot
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
			// Tenant comes from the trusted job record; AddTab drops tabs whose
			// context_id is not one of THIS tenant's contexts.
			tabs[i].TenantID = job.TenantID
			tabs[i].CreatedAt = createdAt
			c.Topology.AddTab(tabs[i])
		}
	}
}

// recordConversationEvent projects a worker-reported conversation-affinity event
// into the conversations store so a reused conversation key resumes the same
// provider chat thread. It is best-effort and nil-safe: a nil manager (feature
// disabled), a job that carries no conversation key, or a store error never
// interrupts ingestion.
//
// Tenant, app, target, and the conversation key are ALWAYS taken from the
// trusted job record Ã¢â‚¬â€ never the worker payload Ã¢â‚¬â€ so a buggy or compromised
// worker cannot bind another tenant's conversation. The ONLY field read from the
// payload is the provider chat URL (conversationThreadRefField); no other payload
// field is ever persisted (redaction), keeping the store within the safe-mode
// constraint (chat URLs only, never cookies/storage state/noVNC URLs).
//
// thread_bound / thread_rebound upsert the binding (idempotent by key, so the
// engine's up-to-3x interaction retries never duplicate a row); thread_broken
// marks it broken so a reused key fails fast instead of resuming a dead chat.
func (c *WorkerConsumer) recordConversationEvent(ctx context.Context, job jobstore.Job, event jobstore.WorkerEvent) {
	if c == nil || c.Conversations == nil {
		return
	}
	conversationKey := strings.TrimSpace(job.ConversationID)
	if conversationKey == "" {
		return
	}
	key := conversations.Key{
		TenantID:        job.TenantID,
		AppID:           job.AppID,
		Target:          job.Target,
		ConversationKey: conversationKey,
	}
	at := event.CreatedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}

	// Resumed turns do not emit thread_bound again. Refresh only the trusted
	// job's activity metadata after its completion was successfully ingested.
	if event.Type == "completed" {
		if err := c.Conversations.Touch(ctx, key, job.ID, at); err != nil {
			slog.Warn("conversation activity projection failed", "job_id", job.ID, "error", err)
		}
		return
	}

	if event.Type == conversationThreadBrokenEventType {
		if _, _, err := c.Conversations.MarkBroken(ctx, key, at); err != nil {
			slog.Warn("conversation thread-broken projection failed",
				"job_id", job.ID, "error", err)
		}
		return
	}

	// thread_bound / thread_rebound Ã¢â€ â€™ upsert. Only the thread URL comes from the
	// payload; every identity field is forced from the job above.
	if _, err := c.Conversations.Bind(ctx, conversations.Conversation{
		TenantID:          job.TenantID,
		AppID:             job.AppID,
		Target:            job.Target,
		ConversationKey:   conversationKey,
		ProviderThreadRef: stringFromEventData(event.Data, conversationThreadRefField),
		State:             conversations.StateActive,
		CreatedAt:         at,
		LastUsedAt:        at,
		LastJobID:         job.ID,
	}); err != nil {
		slog.Warn("conversation thread-bound projection failed",
			"job_id", job.ID, "error", err)
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

func (c *WorkerConsumer) runWorkerWithCancellation(ctx context.Context, lease WorkerLease, execToken string, laneHold *topology.LaneHold, envelope DispatchEnvelope, ingest *streamIngest) ([]jobstore.WorkerEvent, error) {
	return c.superviseRun(ctx, lease, execToken, laneHold, envelope.JobID, false, func(runCtx context.Context) ([]jobstore.WorkerEvent, error) {
		return c.runWorker(runCtx, envelope, ingest)
	})
}

// superviseRun runs one attempt (run) while a watcher keeps its leases alive and
// stops it when the job ends under it. remote marks a run on a Helper Node: its
// cancel does not poll the store at 250 ms. It listens for the in-process cancel
// hint (Cancels) and keeps only the slow safety-net read for a cancel written by
// another process.
func (c *WorkerConsumer) superviseRun(ctx context.Context, lease WorkerLease, execToken string, laneHold *topology.LaneHold, jobID string, remote bool, run func(context.Context) ([]jobstore.WorkerEvent, error)) ([]jobstore.WorkerEvent, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Hub on: wake on this job's commits and keep a slow fallback Get so a
		// cancel landing between wakes (or written by another gateway) is
		// still seen. Hub off: legacy 250 ms poll.
		cancelPoll := 250 * time.Millisecond
		var jobWake <-chan struct{}
		if waker, ok := c.Jobs.(jobstore.JobWaker); ok {
			if wake, unsubscribe, on := waker.SubscribeJobWake(jobID); on {
				defer unsubscribe()
				jobWake, cancelPoll = wake, cancelWatchFallback
			}
		}
		var cancelHint <-chan struct{}
		if remote {
			cancelPoll = max(cancelPoll, cancelWatchFallback)
			var release func()
			cancelHint, release = c.Cancels.Watch(jobID)
			defer release()
		}
		ticker := time.NewTicker(cancelPoll)
		defer ticker.Stop()
		heartbeat := time.NewTicker(c.heartbeatInterval())
		defer heartbeat.Stop()
		beater, _ := lease.(LeaseHeartbeater)
		for {
			select {
			case <-runCtx.Done():
				return
			case <-heartbeat.C:
				// Keep the queue message and the execution lease alive for as
				// long as the job runs.
				if beater != nil {
					if err := beater.Heartbeat(runCtx); err != nil && runCtx.Err() == nil {
						lost := errors.Is(err, ErrLeaseLost)
						helpermetrics.RecordLeaseRenewFailure(helpermetrics.LeaseQueue, renewReason(lost))
						if lost {
							cancel()
							return
						}
					}
				}
				if execToken != "" {
					if err := c.ExecLeases.RenewToken(runCtx, execToken, execLeaseTTL, time.Now().UTC()); err != nil && runCtx.Err() == nil {
						lost := errors.Is(err, topology.ErrTokenLost)
						helpermetrics.RecordLeaseRenewFailure(helpermetrics.LeaseExec, renewReason(lost))
						if lost {
							// We no longer own the job: stop rather than race a
							// second worker onto the same provider submission.
							cancel()
							return
						}
					}
				}
				// A shared browser-lane registration that lapsed (the store was
				// unreachable for its whole TTL) means a voice session may now hold
				// the browser: stop rather than run under a live call.
				if err := laneHold.Renew(runCtx); errors.Is(err, topology.ErrTokenLost) {
					cancel()
					return
				}
			case <-jobWake:
				if c.jobCanceled(runCtx, jobID) {
					cancel()
					return
				}
			case <-cancelHint:
				// The cancel API is running in this process: stop the helper as soon as
				// the store shows the cancel (it is written just after the hint).
				if c.jobCanceledWithin(runCtx, jobID, cancelHintWindow) {
					cancel()
					return
				}
			case <-ticker.C:
				job, found, err := c.Jobs.Get(runCtx, jobID)
				if err != nil || !found {
					continue
				}
				if jobstore.TerminalStatus(job.Status) {
					cancel()
					return
				}
			}
		}
	}()

	events, err := run(runCtx)
	cancel()
	<-done
	return events, err
}

// cancelWatchFallback is the safety-net Get cadence while the event hub is on.
const cancelWatchFallback = 2 * time.Second

// cancelHintWindow bounds how long a cancel hint is verified against the store:
// the cancel API calls CancelJob (the hint) and then writes the canceled status,
// so the first look can come early. A hint the store never confirms (the cancel
// failed) stops nothing.
const cancelHintWindow = 3 * time.Second

// jobCanceledWithin polls the store until the job is terminal or window passes.
func (c *WorkerConsumer) jobCanceledWithin(ctx context.Context, jobID string, window time.Duration) bool {
	expire := time.NewTimer(window)
	defer expire.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if c.jobCanceled(ctx, jobID) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-expire.C:
			return false
		case <-tick.C:
		}
	}
}

const (
	defaultOverloadRetryDelay = 2 * time.Second
	maxOverloadRetryDelay     = 30 * time.Second
)

// retryAfterDelay returns a leased job that could not start (a saturated
// DaemonPool refused its placement, or a live voice session holds its browser)
// to the queue AFTER a delay. The delay is what keeps either from becoming a busy
// loop: the file spool's Retry re-queues instantly and wakes the poller, so
// lease -> refuse -> Retry -> lease would spin. The job stays leased while it
// waits, so no other consumer picks it up early. It is a nack, never an ack: the
// job is not failed or completed, and its status stays what it was.
func (c *WorkerConsumer) retryAfterDelay(ctx context.Context, lease WorkerLease, cause error) (bool, error) {
	delay, reason := defaultOverloadRetryDelay, "overloaded"
	var overload *PoolOverloadError
	var busy *LaneBusyError
	var helperHold *HelperRetryError
	switch {
	case errors.As(cause, &overload):
		reason = overload.Reason
		if overload.RetryAfter > 0 {
			delay = min(overload.RetryAfter, maxOverloadRetryDelay)
		}
	case errors.As(cause, &busy):
		reason = busy.Reason
		if busy.RetryAfter > 0 {
			delay = min(busy.RetryAfter, maxOverloadRetryDelay)
		}
	case errors.As(cause, &helperHold):
		reason = helperHold.Reason
		if helperHold.RetryAfter > 0 {
			delay = min(helperHold.RetryAfter, maxOverloadRetryDelay)
		}
	}
	level := slog.LevelWarn
	if (busy != nil && busy.Reason != laneStateUnavailable) || helperHold != nil {
		level = slog.LevelInfo // an expected wait (behind a call, or for a helper), repeated every delay; not an alarm
	}
	slog.Log(ctx, level, "worker placement refused; retrying the lease after a delay",
		"job_id", lease.JobID(), "reason", reason, "delay", delay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	err := lease.Retry(ctx)
	if err == nil {
		// A shutdown during the hold ends the consumer loop: without it a queue
		// that always has the held job would be leased and refused forever.
		err = ctx.Err()
	}
	return true, err
}

// jobCanceled reports whether the job reached ANY terminal state under the
// running worker (cancel, or a stale-job reaper timeout), so the run stops.
func (c *WorkerConsumer) jobCanceled(ctx context.Context, jobID string) bool {
	job, found, err := c.Jobs.Get(ctx, jobID)
	return err == nil && found && jobstore.TerminalStatus(job.Status)
}

func (c *WorkerConsumer) applyFailure(ctx context.Context, lease WorkerLease, envelope DispatchEnvelope, cause error) error {
	data := map[string]any{
		"status":      string(jobstore.StatusFailedRetryable),
		"retryable":   true,
		"error_class": "worker_execution",
		"message":     sanitizeWorkerError(cause),
	}
	eventType := "failed"
	if errors.Is(cause, ErrAmbiguous) {
		// Same terminal shape the worker emits for a post-submit failure
		// (engine.py, UBAG_WORKER_STRICT_SUBMIT): never retryable, reconcile.
		eventType = "failed_terminal"
		data = map[string]any{
			"status":             string(jobstore.StatusFailedTerminal),
			"retryable":          false,
			"error_class":        "worker_execution",
			"message":            "worker failed after prompt submission; reconcile required",
			"reason":             "post_submit_failure",
			"submitted":          true,
			"reconcile_required": true,
			"stream_end_reason":  "error",
		}
	}
	event := jobstore.WorkerEvent{
		EventID:    failureEventID(lease, envelope),
		JobID:      lease.JobID(),
		APIVersion: envelope.APIVersion,
		Type:       eventType,
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
	// Release the in-flight concurrency token acquired for this job at creation.
	// Every path that reaches this helper does so via applyFailure, which drives
	// the job to a terminal failure status; releasing here closes the token leak
	// on the worker failure paths. Release is per-job and idempotent
	// (ReleaseForJob), so overlapping with any other terminal owner Ã¢â‚¬â€ the RunOnce
	// completed/cancelled branches or the cancel API Ã¢â‚¬â€ cannot double-count the
	// shared lane.
	if jobstore.TerminalStatus(job.Status) {
		c.observeTerminalJob(job)
		c.releaseConcurrencyToken(job)
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

// finishTerminalLeasedJob runs the terminal-job closing sequence for a job
// already known to be terminal when leased: observe, post-job hook, notify,
// then Cancel for canceled jobs and Complete otherwise. The two RunOnce
// pre-execution paths shared this block verbatim; one copy means the next
// step added here cannot be forgotten on one path.
func (c *WorkerConsumer) finishTerminalLeasedJob(ctx context.Context, lease WorkerLease, job jobstore.Job) (bool, error) {
	c.observeTerminalJob(job)
	c.runPostJobHook(ctx, job)
	if err := c.notifyTerminalJob(ctx, lease, job); err != nil {
		return true, err
	}
	if job.Status == jobstore.StatusCanceled {
		return true, lease.Cancel(ctx)
	}
	return true, lease.Complete(ctx)
}

// finishTerminalIngestedJob runs the terminal-job closing sequence after
// worker ingestion, releasing the in-flight token first: observe, release,
// post-job hook, notify, then the given lease finisher (Cancel, Complete, or
// Fail). The three post-ingestion branches differed only in the finisher.
func (c *WorkerConsumer) finishTerminalIngestedJob(ctx context.Context, lease WorkerLease, job jobstore.Job, finish func(context.Context) error) (bool, error) {
	c.observeTerminalJob(job)
	c.releaseConcurrencyToken(job)
	c.runPostJobHook(ctx, job)
	if err := c.notifyTerminalJob(ctx, lease, job); err != nil {
		return true, err
	}
	return true, finish(ctx)
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

// materializeAttachments writes every declared job attachment (input.attachments,
// plus the back-compat input.audio_artifact_key alias) to a local temp file and
// injects input.attachment_local_paths (aligned to the declared order) so the
// worker subprocess can attach the files. For the single audio alias it also sets
// input.audio_local_path, keeping the pre-existing dictation path working. The
// gateway already holds the bytes in its artifact store, so it writes them locally
// and hands the worker paths only Ã¢â‚¬â€ the worker never needs gateway credentials.
//
// It returns one cleanup func that removes all temp files. It is a no-op
// (nil, nil) when the runner has no store or the job declares no attachments Ã¢â‚¬â€ so
// text jobs are completely unaffected. It fails closed: an invalid key or a
// missing/unreadable artifact returns an error (and cleans up any temp files it
// already wrote) rather than silently attaching nothing. Because the dispatch
// gate only enqueues a job once every declared key is present, a missing artifact
// here means the gate was bypassed Ã¢â‚¬â€ a bug, not a normal race.
func (r ProcessWorkerRunner) materializeAttachments(ctx context.Context, envelope *DispatchEnvelope) (func(), error) {
	if r.Artifacts == nil || envelope == nil || envelope.Job.Input == nil {
		return nil, nil
	}
	declared, err := attachments.DeclaredAttachments(envelope.Job.Input)
	if err != nil {
		recordAttachmentMaterializeFailure("manifest_invalid")
		return nil, fmt.Errorf("materialize attachments: %w", err)
	}
	if len(declared) == 0 {
		return nil, nil
	}

	tmpPaths := make([]string, 0, len(declared))
	tmpDirs := make([]string, 0, len(declared))
	cleanup := func() {
		for _, p := range tmpPaths {
			_ = os.Remove(p)
		}
		for _, dir := range tmpDirs {
			_ = os.Remove(dir)
		}
	}

	localPaths := make([]any, 0, len(declared))
	pathByKey := make(map[string]string, len(declared))
	for _, att := range declared {
		key := att.Key
		if !attachments.ValidKey(key) {
			cleanup()
			recordAttachmentMaterializeFailure("invalid_key")
			return nil, fmt.Errorf("materialize attachment: invalid key %q", key)
		}
		rc, record, err := r.Artifacts.GetArtifact(ctx, envelope.JobID, key)
		if err != nil {
			cleanup()
			recordAttachmentMaterializeFailure("artifact_read")
			return nil, fmt.Errorf("materialize attachment %q: %w", key, err)
		}
		filename := materializedAttachmentFilename(att, record.ContentType)
		tmpDir, err := os.MkdirTemp("", "ubag-attach-*")
		if err != nil {
			_ = rc.Close()
			cleanup()
			recordAttachmentMaterializeFailure("temp_create")
			return nil, fmt.Errorf("create temp attachment directory: %w", err)
		}
		tmpDirs = append(tmpDirs, tmpDir)
		tmpPath := filepath.Join(tmpDir, filename)
		if linkArtifactLocally(ctx, r.Artifacts, envelope.JobID, key, tmpPath) {
			_ = rc.Close()
			tmpPaths = append(tmpPaths, tmpPath)
			localPaths = append(localPaths, tmpPath)
			pathByKey[key] = tmpPath
			continue
		}
		tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = rc.Close()
			cleanup()
			recordAttachmentMaterializeFailure("temp_create")
			return nil, fmt.Errorf("create temp attachment file: %w", err)
		}
		tmpPaths = append(tmpPaths, tmpPath)
		if _, err := io.Copy(tmp, rc); err != nil {
			_ = tmp.Close()
			_ = rc.Close()
			cleanup()
			recordAttachmentMaterializeFailure("artifact_write")
			return nil, fmt.Errorf("write attachment %q: %w", key, err)
		}
		if err := tmp.Close(); err != nil {
			_ = rc.Close()
			cleanup()
			recordAttachmentMaterializeFailure("temp_close")
			return nil, fmt.Errorf("close temp attachment file: %w", err)
		}
		_ = rc.Close()
		localPaths = append(localPaths, tmpPath)
		pathByKey[key] = tmpPath
	}

	envelope.Job.Input["attachment_local_paths"] = localPaths
	// Back-compat: keep audio_local_path set for the single dictation-audio path
	// the worker's live engine already reads.
	if audioKey, ok := envelope.Job.Input["audio_artifact_key"].(string); ok {
		if p, ok := pathByKey[strings.TrimSpace(audioKey)]; ok {
			envelope.Job.Input["audio_local_path"] = p
		}
	}
	return cleanup, nil
}

// MaterializeAttachments is materializeAttachments for callers outside this
// package: a Helper Node injects its read-only staging ArtifactStore (perf-fleet
// P4.10) as Artifacts and reuses the same fail-closed temp-file logic, so the
// ubag-attach- temp prefix the worker's path guard requires stays in one place.
func (r ProcessWorkerRunner) MaterializeAttachments(ctx context.Context, envelope *DispatchEnvelope) (func(), error) {
	return r.materializeAttachments(ctx, envelope)
}

// attachmentHardlinkEnabled gates the hardlink fast path. Off by default: the
// copy path is the long-standing behaviour. A hardlink shares the artifact's
// inode, so it is only safe because the worker reads attachments (browser file
// upload) and never writes to them.
func attachmentHardlinkEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_ATTACHMENT_HARDLINK"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// linkArtifactLocally hardlinks the artifact's on-disk object to dst when the
// flag is on and the store keeps objects as local files. It reports false on any
// doubt (flag off, store without local paths, cross-device, link refused) so the
// caller falls back to the streaming copy. The path comes from the same
// tenant/job-scoped lookup GetArtifact uses.
func linkArtifactLocally(ctx context.Context, store artifacts.ArtifactStore, jobID, key, dst string) bool {
	if !attachmentHardlinkEnabled() {
		return false
	}
	pather, ok := store.(artifacts.LocalObjectPather)
	if !ok {
		return false
	}
	src, _, err := pather.LocalObjectPath(ctx, jobID, key)
	if err != nil {
		return false
	}
	return os.Link(src, dst) == nil
}

func materializedAttachmentFilename(att attachments.Attachment, contentType string) string {
	return attachments.MaterializedFilename(att, contentType)
}

func extForContentType(contentType string) string { return attachments.ExtForContentType(contentType) }

func (r ProcessWorkerRunner) RunWorker(ctx context.Context, envelope DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
	if envelope.Job.Target == "antigravity_cli" && !antigravity.OAuthEnabled() {
		return nil, fmt.Errorf("Antigravity OAuth is disabled")
	}
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

	// Materialize any declared attachments (documents/images/audio/video/voice)
	// to local temp files the worker subprocess can attach. The gateway already
	// holds the bytes in its artifact store, so it writes them locally and injects
	// attachment_local_paths (and audio_local_path for the single-audio alias) Ã¢â‚¬â€
	// the worker never needs gateway credentials. No-op for text jobs.
	cleanupAttachments, err := r.materializeAttachments(runCtx, &envelope)
	if err != nil {
		return nil, err
	}
	if cleanupAttachments != nil {
		defer cleanupAttachments()
	}

	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}

	run := func(accountSocket string) ([]jobstore.WorkerEvent, error) {
		command := exec.CommandContext(runCtx, python, script, "--input", "-")
		command.Stdin = bytes.NewReader(payload)
		stdout := &limitedBuffer{max: maxWorkerOutputBytes}
		stderr := &limitedBuffer{max: maxWorkerStderrBytes}
		// Failures are typed from the prompt_submitted marker already printed.
		fail := func(err error) error { return classifySubmission(err, jsonlHasPromptSubmitted(stdout.Bytes())) }
		command.Stdout = stdout
		command.Stderr = stderr
		command.Env = workerEnvForTarget(envelope.Job.Target)
		if accountSocket != "" {
			command.Env = append(command.Env, "UBAG_ANTIGRAVITY_ACCOUNT_SOCKET="+accountSocket)
		}
		if err := command.Run(); err != nil {
			if runCtx.Err() == context.DeadlineExceeded {
				return nil, fail(fmt.Errorf("worker process timed out after %s", maxRuntime))
			}
			if runCtx.Err() == context.Canceled {
				return nil, fail(context.Canceled)
			}
			slog.Error("worker process failed", "stderr", stderr.buf.String(), "stdout_bytes", stdout.buf.Len(), "error", err)
			return nil, fail(fmt.Errorf("worker process failed"))
		}
		if stdout.truncated {
			return nil, fail(fmt.Errorf("worker stdout exceeded %d bytes", maxWorkerOutputBytes))
		}
		events, err := parseWorkerJSONL(stdout.Bytes())
		if err != nil {
			return nil, fail(err)
		}
		return events, nil
	}
	if envelope.Job.Target != "antigravity_cli" {
		return run("")
	}
	store := r.AntigravityStore
	if store == nil {
		store = antigravity.GetStore()
	}
	socketDir := strings.TrimSpace(os.Getenv("UBAG_ANTIGRAVITY_SOCKET_DIR"))
	if socketDir == "" {
		return nil, fmt.Errorf("isolated account socket directory is not configured")
	}
	return runIsolatedCLIAttempts(runCtx, store, r.Jobs, envelope, func(account antigravity.Account) (string, bool) {
		if filepath.Base(account.ID) != account.ID || account.ID == "." {
			return "", false
		}
		socketPath := antigravity.AccountSocketPath(socketDir, account.ID)
		info, err := os.Lstat(socketPath)
		return socketPath, err == nil && info.Mode()&os.ModeSocket != 0
	}, run)
}

func runIsolatedCLIAttempts(
	ctx context.Context, store *antigravity.Store, jobs jobstore.Store, envelope DispatchEnvelope,
	accountSocket func(antigravity.Account) (string, bool),
	run func(string) ([]jobstore.WorkerEvent, error),
) ([]jobstore.WorkerEvent, error) {
	requestedAccount, _ := envelope.Job.Options["antigravity_account_id"].(string)
	var quotaEvents []jobstore.WorkerEvent
	for _, account := range store.EligibleAccounts(envelope.TenantID, time.Now()) {
		if requestedAccount != "" && account.ID != requestedAccount {
			continue
		}
		if !accountVerifiedForJob(ctx, jobs, envelope, account) {
			continue
		}
		socket, ready := accountSocket(account)
		if !ready {
			continue
		}
		events, err := run(socket)
		if err != nil {
			return nil, err
		}
		if upfrontQuotaError(events) {
			if err := store.MarkAccountExhausted(envelope.TenantID, account.ID, antigravity.DefaultCooldownSec); err != nil {
				return events, nil
			}
			quotaEvents = events
			if requestedAccount != "" {
				break
			}
			continue
		}
		if err := store.MarkAccountUsed(envelope.TenantID, account.ID); err != nil {
			slog.Error("failed to persist Antigravity account last-used time", "error", err)
		}
		return events, nil
	}
	if quotaEvents != nil {
		return quotaEvents, nil
	}
	return nil, fmt.Errorf("no isolated account socket available for tenant")
}

func accountVerifiedForJob(ctx context.Context, jobs jobstore.Store, envelope DispatchEnvelope, account antigravity.Account) bool {
	if jobs == nil || account.VerificationJobID == "" {
		return false
	}
	canary, found, err := jobs.Get(ctx, account.VerificationJobID)
	if err != nil || !found || canary.ID != account.VerificationJobID || canary.TenantID != envelope.TenantID ||
		canary.Client["app_id"] != "antigravity-test" ||
		canary.Target != "antigravity_cli" || canary.Options["antigravity_account_id"] != account.ID {
		return false
	}
	if canary.ID == envelope.JobID {
		return envelope.AppID == canary.AppID && envelope.Client["app_id"] == canary.Client["app_id"] &&
			envelope.Job.Target == "antigravity_cli" &&
			envelope.Job.Options["antigravity_account_id"] == account.ID && !jobstore.TerminalStatus(canary.Status)
	}
	return canary.Status == jobstore.StatusCompleted
}

func upfrontQuotaError(events []jobstore.WorkerEvent) bool {
	for index, event := range events {
		if event.Type == "failed" && index == len(events)-1 {
			return event.Data["error_code"] == "OAUTH_QUOTA_EXHAUSTED"
		}
		if event.Type != "queued" && event.Type != "started" {
			return false
		}
	}
	return false
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

// attemptEventIDsEnabled gates attempt-scoped event ids (default off).
func attemptEventIDsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("UBAG_WORKER_ATTEMPT_EVENT_IDS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// failureEventID is stable per (job, lease); with an attempt it also carries
// the attempt id so a retry attempt's failure is never deduped away.
func failureEventID(lease WorkerLease, envelope DispatchEnvelope) string {
	id := "gateway_worker_failure:" + lease.JobID() + ":" + lease.LeaseID()
	if envelope.Attempt != nil && envelope.Attempt.ID != "" {
		id += ":" + envelope.Attempt.ID
	}
	return id
}

// dropStaleAttemptEvents drops events stamped (data.attempt_id) by a different
// attempt than the one this envelope leased. Unstamped events pass (mixed-id
// tolerance across a deploy).
func dropStaleAttemptEvents(envelope DispatchEnvelope, events []jobstore.WorkerEvent) []jobstore.WorkerEvent {
	if envelope.Attempt == nil || envelope.Attempt.ID == "" {
		return events
	}
	kept := make([]jobstore.WorkerEvent, 0, len(events))
	for _, event := range events {
		if staleAttemptEvent(envelope, event) {
			continue
		}
		kept = append(kept, event)
	}
	return kept
}

// staleAttemptEvent reports whether event is stamped (data.attempt_id) by a
// different attempt than the one this envelope leased.
func staleAttemptEvent(envelope DispatchEnvelope, event jobstore.WorkerEvent) bool {
	if envelope.Attempt == nil || envelope.Attempt.ID == "" {
		return false
	}
	id, _ := event.Data["attempt_id"].(string)
	return id != "" && id != envelope.Attempt.ID
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

// EnqueueNotify surfaces the spool dispatcher's enqueue wake channel so the
// lease loop can select on it (changeNotifier).
func (q fileSpoolWorkerQueue) EnqueueNotify() <-chan struct{} {
	return q.spool.EnqueueNotify()
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

// Heartbeat renews the spool lease (no-op without UBAG_EXECUTOR_LEASE_TTL_MS).
func (l fileSpoolWorkerLease) Heartbeat(ctx context.Context) error {
	return l.spool.RenewLease(ctx, l.lease)
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
	// Optional JSON objects are tagged omitempty in the dispatch envelope.
	// Queue serialization therefore turns an empty persisted map into an
	// omitted field (nil on decode). Both represent the same JSON object state;
	// rejecting that round-trip poisons a valid lease and stops the consumer.
	if nilOrEmptyJSONObject(actual) && nilOrEmptyJSONObject(expected) {
		return true
	}
	actualJSON, actualErr := json.Marshal(actual)
	expectedJSON, expectedErr := json.Marshal(expected)
	return actualErr == nil && expectedErr == nil && string(actualJSON) == string(expectedJSON)
}

func nilOrEmptyJSONObject(value any) bool {
	if value == nil {
		return true
	}
	object, ok := value.(map[string]any)
	return ok && len(object) == 0
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

// workerEnvAllowed is the shared worker env allowlist (local and helper).
var workerEnvAllowed = workerdaemon.AllowedEnv

func minimalWorkerEnv() []string { return filteredWorkerEnv(nil) }

// filteredWorkerEnv returns the allowlisted environment minus the keys in deny
// (upper-case). deny nil == the local worker env, unchanged.
func filteredWorkerEnv(deny map[string]struct{}) []string { return workerdaemon.FilterEnv(deny) }

func workerEnvForTarget(target string) []string {
	env := minimalWorkerEnv()
	if target == "antigravity_cli" {
		filtered := env[:0]
		for _, item := range env {
			key, _, _ := strings.Cut(item, "=")
			if !strings.EqualFold(key, "HOME") && !strings.EqualFold(key, "USERPROFILE") {
				filtered = append(filtered, item)
			}
		}
		env = filtered
	}
	if target != "antigravity_sdk" && target != "antigravity_cli" {
		return env
	}

	keys := []string{
		"UBAG_ANTIGRAVITY_ENABLED", "UBAG_ANTIGRAVITY_MODEL", "UBAG_ANTIGRAVITY_EFFORT",
	}
	if target == "antigravity_sdk" {
		keys = append(keys,
			"GEMINI_API_KEY", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION",
			"UBAG_ANTIGRAVITY_MAX_TOKENS", "UBAG_ANTIGRAVITY_MAX_MODEL_CALLS", "UBAG_ANTIGRAVITY_MAX_TOOL_CALLS",
			"UBAG_ANTIGRAVITY_COMPACTION", "UBAG_ANTIGRAVITY_TOOL_OUTPUT_MAX_CHARS", "UBAG_ANTIGRAVITY_RETRY_MAX",
		)
	}
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}
