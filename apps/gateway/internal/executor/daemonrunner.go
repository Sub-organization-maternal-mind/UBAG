package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/workerdaemon"
)

// daemonJobEndKey marks the terminal control line of a job. It mirrors JOB_END
// in ubag_worker/live/daemon_protocol.py -- keep the two in sync.
const daemonJobEndKey = workerdaemon.JobEndKey

// DaemonWorkerRunner drives jobs through ONE long-lived worker process instead of
// spawning a worker per job (ProcessWorkerRunner). The daemon keeps browser pages
// warm between jobs, so a job stops paying process startup, CDP re-attach, and a
// cold SPA load.
//
// Opt-in via UBAG_WORKER_DAEMON; unset keeps the per-job spawn.
//
// One job at a time, enforced by mu: the daemon holds a single browser profile,
// and concurrent turns on one provider account risk interleaved output and
// CAPTCHA/lockout. That is also why the daemon itself runs one job at a time --
// this mutex is the Go half of the same invariant. A DaemonPool
// (UBAG_WORKER_POOL_SIZE > 1) owns several of these as isolated slots and decides
// which job goes to which slot; each slot still runs exactly one job at a time.
type DaemonWorkerRunner struct {
	Python     string
	Script     string
	MaxRuntime time.Duration
	Artifacts  artifacts.ArtifactStore

	// newCommand builds the daemon process. Overridable so tests can re-exec the
	// test binary as a fake daemon instead of depending on a Python interpreter.
	newCommand func() *exec.Cmd

	// slotMode, slotID and poolSize are set only by DaemonPool. A slot daemon is
	// spawned with UBAG_WORKER_SLOT_ID / UBAG_WORKER_POOL_SIZE so the worker scopes
	// its page registry per slot, takes the cross-process identity lock and reaps
	// the registries of slots beyond the pool size (P3.3). The zero value is the
	// single legacy daemon, unchanged from before the pool existed.
	slotMode bool
	slotID   int
	poolSize int

	procOnce sync.Once
	proc     *workerdaemon.Process
}

// process is the supervised daemon behind this runner, built on first use so the
// exported fields and test hooks can be set after construction.
func (r *DaemonWorkerRunner) process() *workerdaemon.Process {
	r.procOnce.Do(func() {
		r.proc = &workerdaemon.Process{
			Python: r.Python, Script: r.Script, NewCommand: r.newCommand,
			SlotMode: r.slotMode, SlotID: r.slotID, PoolSize: r.poolSize,
		}
	})
	return r.proc
}

// runDaemonJob is the batch form of the protocol: it buffers every line and
// parses them only after the terminal marker.
//
// A stream that ends without a marker is an error, never a success: the daemon
// died mid-job, and returning the events collected so far would hand back a
// TRUNCATED report as though it were complete.
func runDaemonJob(
	stdin io.Writer,
	stdout *bufio.Reader,
	envelope DispatchEnvelope,
	maxRuntime time.Duration,
) ([]jobs.WorkerEvent, error) {
	return runDaemonJobTracked(stdin, stdout, envelope, maxRuntime, nil)
}

// runDaemonJobTracked is runDaemonJob that also records into submitted (when
// non-nil) that a prompt_submitted line went by.
func runDaemonJobTracked(
	stdin io.Writer,
	stdout *bufio.Reader,
	envelope DispatchEnvelope,
	maxRuntime time.Duration,
	submitted *atomic.Bool,
) ([]jobs.WorkerEvent, error) {
	var body bytes.Buffer
	err := readDaemonJob(stdin, stdout, envelope, maxRuntime, submitted, func(line string) error {
		body.WriteString(line)
		body.WriteByte('\n')
		return nil
	})
	if err != nil {
		return nil, err
	}
	return parseWorkerJSONL(body.Bytes())
}

// streamDaemonJob is the incremental form: each non-control line is parsed and
// emitted to the sink as it arrives. The marker rules are identical, so a
// stream that stops without one (or ends failed) returns an error and the
// consumer discards whatever the sink already saw.
func streamDaemonJob(
	ctx context.Context,
	stdin io.Writer,
	stdout *bufio.Reader,
	envelope DispatchEnvelope,
	maxRuntime time.Duration,
	sink EventSink,
) error {
	return streamDaemonJobTracked(ctx, stdin, stdout, envelope, maxRuntime, sink, nil)
}

func streamDaemonJobTracked(
	ctx context.Context,
	stdin io.Writer,
	stdout *bufio.Reader,
	envelope DispatchEnvelope,
	maxRuntime time.Duration,
	sink EventSink,
	submitted *atomic.Bool,
) error {
	return readDaemonJob(stdin, stdout, envelope, maxRuntime, submitted, func(line string) error {
		var event jobs.WorkerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return fmt.Errorf("worker emitted malformed JSONL")
		}
		return sink.Emit(ctx, event)
	})
}

// readDaemonJob is the protocol core (workerdaemon.ReadJob): one request line
// out, then lines in (each handed to onLine, trimmed and non-empty) until the
// terminal marker. Split out from process management so it is testable without
// spawning anything.
func readDaemonJob(
	stdin io.Writer,
	stdout *bufio.Reader,
	envelope DispatchEnvelope,
	maxRuntime time.Duration,
	submitted *atomic.Bool,
	onLine func(line string) error,
) error {
	var observe func(string)
	if submitted != nil {
		observe = func(line string) {
			if lineIsPromptSubmitted(line) {
				submitted.Store(true)
			}
		}
	}
	return workerdaemon.ReadJob(stdin, stdout, workerdaemon.Request{
		JobID:     envelope.JobID,
		DeadlineS: maxRuntime.Seconds(),
		Payload:   envelope,
	}, maxWorkerOutputBytes, observe, onLine)
}

func readBoundedDaemonLine(reader *bufio.Reader, limit int) (string, error) {
	return workerdaemon.ReadBoundedLine(reader, limit)
}

func (r *DaemonWorkerRunner) buildCommand() *exec.Cmd { return r.process().BuildCommand() }

// daemonJobFunc runs the protocol for one job on an already-started daemon.
// submitted records that a prompt_submitted line went by (UBAG_WORKER_STRICT_SUBMIT).
type daemonJobFunc func(ctx context.Context, stdin io.Writer, stdout *bufio.Reader, envelope DispatchEnvelope, maxRuntime time.Duration, submitted *atomic.Bool) error

// batchDaemonJob is the protocol step behind RunWorker: events are buffered into
// *events and only valid once the job reports a clean end.
func batchDaemonJob(events *[]jobs.WorkerEvent) daemonJobFunc {
	return func(_ context.Context, stdin io.Writer, stdout *bufio.Reader, env DispatchEnvelope, maxRuntime time.Duration, submitted *atomic.Bool) error {
		var err error
		*events, err = runDaemonJobTracked(stdin, stdout, env, maxRuntime, submitted)
		return err
	}
}

// streamingDaemonJob is the protocol step behind StreamWorker.
func streamingDaemonJob(sink EventSink) daemonJobFunc {
	return func(runCtx context.Context, stdin io.Writer, stdout *bufio.Reader, env DispatchEnvelope, maxRuntime time.Duration, submitted *atomic.Bool) error {
		return streamDaemonJobTracked(runCtx, stdin, stdout, env, maxRuntime, sink, submitted)
	}
}

// RunWorker implements WorkerRunner.
func (r *DaemonWorkerRunner) RunWorker(
	ctx context.Context, envelope DispatchEnvelope,
) ([]jobs.WorkerEvent, error) {
	var events []jobs.WorkerEvent
	if err := r.runJob(ctx, envelope, batchDaemonJob(&events)); err != nil {
		return nil, err
	}
	return events, nil
}

// StreamWorker implements StreamingWorkerRunner: same process management and
// failure handling as RunWorker, events emitted to the sink as they arrive.
func (r *DaemonWorkerRunner) StreamWorker(
	ctx context.Context, envelope DispatchEnvelope, sink EventSink,
) error {
	return r.runJob(ctx, envelope, streamingDaemonJob(sink))
}

func (r *DaemonWorkerRunner) maxRuntime() time.Duration {
	if r.MaxRuntime <= 0 {
		return defaultWorkerMaxRuntime
	}
	return r.MaxRuntime
}

// materializeDaemonAttachments writes a job's declared attachments to local temp
// files exactly as the per-job runner does, so attachment jobs behave identically
// under the daemon. Materialization has its own maxRuntime budget: it is not part
// of the job's run timeout, which only starts once a daemon slot is held.
func materializeDaemonAttachments(
	ctx context.Context,
	store artifacts.ArtifactStore,
	envelope *DispatchEnvelope,
	maxRuntime time.Duration,
) (func(), error) {
	attachCtx, cancel := context.WithTimeout(ctx, maxRuntime)
	defer cancel()
	return ProcessWorkerRunner{Artifacts: store}.materializeAttachments(attachCtx, envelope)
}

// runJob owns everything around one daemon job (attachments, the one-job mutex,
// deadline, lazy spawn, discard-on-failure); job runs the protocol.
func (r *DaemonWorkerRunner) runJob(
	ctx context.Context,
	envelope DispatchEnvelope,
	job daemonJobFunc,
) (err error) {
	// With UBAG_WORKER_STRICT_SUBMIT every failure is typed ErrNotSubmitted or
	// ErrAmbiguous from the prompt_submitted marker this job's stream carried
	// (in-process state only; the durable ledger is P3.8). Flag off: untouched.
	var submitted atomic.Bool
	defer func() { err = classifySubmission(err, submitted.Load()) }()

	maxRuntime := r.maxRuntime()
	cleanupAttachments, err := materializeDaemonAttachments(ctx, r.Artifacts, &envelope, maxRuntime)
	if err != nil {
		return err
	}
	if cleanupAttachments != nil {
		defer cleanupAttachments()
	}
	return r.runExclusive(ctx, envelope, maxRuntime, &submitted, job)
}

// runExclusive runs the job on this runner's daemon, one job at a time. The run
// timeout starts only once the daemon is held (workerdaemon.Process.Run).
func (r *DaemonWorkerRunner) runExclusive(
	ctx context.Context,
	envelope DispatchEnvelope,
	maxRuntime time.Duration,
	submitted *atomic.Bool,
	job daemonJobFunc,
) error {
	return r.process().Run(ctx, maxRuntime, func(runCtx context.Context, stdin io.Writer, stdout *bufio.Reader) error {
		return job(runCtx, stdin, stdout, envelope, maxRuntime, submitted)
	})
}

// Close shuts the daemon down (gateway shutdown).
func (r *DaemonWorkerRunner) Close() { r.process().Close() }

// killForTest kills the daemon process without clearing the runner's handles, so
// a test can assert the next job restarts it the way a real crash would.
func (r *DaemonWorkerRunner) killForTest() { r.process().KillForTest() }
