package executor

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/workerdaemon"
)

// ErrPoolOverloaded is the typed, retryable saturation signal of a DaemonPool: no
// slot (or, for an identity that is already busy, no turn on it) became available
// within the bounded wait, or the placement queue was full. The job did NOT reach
// a worker. The consumer answers it with a delayed lease Retry (ADR-0011), never a
// job failure and never an immediate re-queue.
var ErrPoolOverloaded = workerdaemon.ErrOverloaded

var errDaemonPoolClosed = workerdaemon.ErrClosed

// PoolOverloadError carries why a placement was refused and how long the caller
// should hold the job back before retrying. errors.Is(err, ErrPoolOverloaded).
type PoolOverloadError = workerdaemon.OverloadError

// DaemonPool runs jobs on N isolated warm-daemon slots (UBAG_WORKER_POOL_SIZE)
// instead of one daemon behind one mutex. The placement machinery (identity gate,
// warm affinity, bounded wait, drain) is workerdaemon.Pool, shared with the Helper
// Node; this type adds the envelope-specific parts: the identity key, attachment
// materialization and the typed submission boundary.
//
//   - Each slot is a separate worker process speaking the unchanged
//     one-job-at-a-time stdio protocol. A failure, cancel or deadline kills only
//     that slot's process; the others keep their warm pages.
//   - Identity gate: at most ONE active job per physical browser session (the
//     tenant-free key of ubag_worker/live/identity_lock.py). Two jobs for one
//     provider account never overlap, whatever the slot count. The worker's own
//     flock (slot mode) is the cross-process backstop for keys Go cannot derive
//     exactly.
//   - Warm affinity: a job goes to the idle slot whose page for its identity is
//     still warm; otherwise to a live cold slot, then a never-started slot, and
//     only then does it evict the least recently used warm page.
//   - Bounded wait: a job that cannot be placed waits at most MaxWait (and the
//     placement queue is bounded by MaxQueue), then fails with ErrPoolOverloaded.
//   - The job's run timeout starts only after a slot is held.
//
// A pool of one slot behaves like a DaemonWorkerRunner, which is what the gateway
// still builds by default; the pool is used only when UBAG_WORKER_POOL_SIZE > 1.
type DaemonPool struct {
	Python     string
	Script     string
	MaxRuntime time.Duration
	Artifacts  artifacts.ArtifactStore

	// Size is the number of slots; <= 0 means 1 and it is capped at 32.
	Size int
	// MaxWait is the longest a job waits to be placed before ErrPoolOverloaded.
	// <= 0 means 30s. It is deliberately NOT tied to MaxRuntime (25 minutes in
	// production): a wait that long would pin a consumer worker and make the
	// "bounded" overload unreachable in practice.
	MaxWait time.Duration
	// MaxQueue bounds how many jobs may wait to be placed at once; one more is
	// refused immediately. <= 0 means Size (the consumer runs about Size jobs).
	MaxQueue int
	// RetryAfter is the hold-back hint carried by every overload error. <= 0
	// means 2s.
	RetryAfter time.Duration
	// DrainTimeout bounds how long Close lets a slot exit on its own (stdin EOF)
	// before it is killed. <= 0 means 5s.
	DrainTimeout time.Duration

	// newSlotCommand builds a slot's daemon process; tests re-exec the test binary.
	newSlotCommand func(slot int) *exec.Cmd

	initOnce sync.Once
	pool     *workerdaemon.Pool
}

func (p *DaemonPool) init() {
	p.initOnce.Do(func() {
		p.pool = &workerdaemon.Pool{
			Python: p.Python, Script: p.Script,
			Size: p.Size, MaxWait: p.MaxWait, MaxQueue: p.MaxQueue,
			RetryAfter: p.RetryAfter, DrainTimeout: p.DrainTimeout,
			NewSlotCommand: p.newSlotCommand,
		}
	})
}

func (p *DaemonPool) maxRuntime() time.Duration {
	if p.MaxRuntime <= 0 {
		return defaultWorkerMaxRuntime
	}
	return p.MaxRuntime
}

// RunWorker implements WorkerRunner.
func (p *DaemonPool) RunWorker(
	ctx context.Context, envelope DispatchEnvelope,
) ([]jobs.WorkerEvent, error) {
	var events []jobs.WorkerEvent
	if err := p.run(ctx, envelope, batchDaemonJob(&events)); err != nil {
		return nil, err
	}
	return events, nil
}

// StreamWorker implements StreamingWorkerRunner.
func (p *DaemonPool) StreamWorker(
	ctx context.Context, envelope DispatchEnvelope, sink EventSink,
) error {
	return p.run(ctx, envelope, streamingDaemonJob(sink))
}

// run places the job on a slot (identity gate + affinity), runs it there, and
// frees the slot. Attachments are materialized only once a slot is held, so an
// overloaded job that is retried later does not pay for them again.
func (p *DaemonPool) run(ctx context.Context, envelope DispatchEnvelope, job daemonJobFunc) (err error) {
	// Same typed submission boundary as DaemonWorkerRunner.runJob
	// (UBAG_WORKER_STRICT_SUBMIT; flag off: untouched). A placement refusal never
	// reached a daemon, so with the flag on it reads as ErrNotSubmitted while
	// ErrPoolOverloaded stays in the error chain.
	var submitted atomic.Bool
	defer func() { err = classifySubmission(err, submitted.Load()) }()

	p.init()
	maxRuntime := p.maxRuntime()
	return p.pool.Run(ctx, daemonIdentityKey(envelope), func(slot *workerdaemon.Slot) error {
		cleanup, err := materializeDaemonAttachments(ctx, p.Artifacts, &envelope, maxRuntime)
		if err != nil {
			return err
		}
		if cleanup != nil {
			defer cleanup()
		}
		return slot.Run(ctx, maxRuntime, func(runCtx context.Context, stdin io.Writer, stdout *bufio.Reader) error {
			return job(runCtx, stdin, stdout, envelope, maxRuntime, &submitted)
		})
	})
}

// Close drains the pool (gateway shutdown): new and waiting jobs are refused, and
// every slot finishes its active job and is then asked to exit by closing its
// stdin (the worker closes its warm pages itself), with a kill after DrainTimeout.
func (p *DaemonPool) Close() {
	p.init()
	p.pool.Close()
}

// daemonIdentityKey is the identity-gate key: one active job per PHYSICAL browser
// session. It is deliberately tenant-free (two tenants mapped to one provider
// account must serialize) and is the Go twin of physical_session_key in
// ubag_worker/live/identity_lock.py: the CDP endpoint when one is configured,
// else the requested profile directory, plus the target.
//
// The endpoint form hashes to exactly the worker's value. The profile-directory
// form is cleaned but not symlink-resolved, so an alias of the same directory can
// look like two identities here; the worker's flock then serializes them. Jobs
// that carry no profile hint share the target's default profile and therefore one
// key per target.
func daemonIdentityKey(envelope DispatchEnvelope) string {
	where := "dir:" + daemonProfileHint(envelope.Job)
	if endpoint := strings.TrimSpace(os.Getenv("UBAG_REMOTE_BROWSER_ENDPOINT")); endpoint != "" {
		where = "cdp:" + strings.ToLower(strings.TrimRight(endpoint, "/"))
	}
	return workerdaemon.IdentityKey(where, envelope.Job.Target)
}

// daemonProfileHint is the profile directory a job asks for, using the same
// fields and precedence as the worker's _resolve_user_data_dir (options, then
// context); "" means the target's default profile.
func daemonProfileHint(job DispatchJob) string {
	for _, source := range []map[string]any{job.Options, job.Context} {
		for _, field := range []string{"user_data_dir", "profile_dir", "profile_path"} {
			if value, ok := source[field].(string); ok && strings.TrimSpace(value) != "" {
				return path.Clean(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
			}
		}
	}
	return ""
}
