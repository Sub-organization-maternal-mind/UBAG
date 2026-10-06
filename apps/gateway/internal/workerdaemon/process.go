package workerdaemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// MaxStderrBytes bounds the stderr a daemon may buffer (it is never forwarded).
const MaxStderrBytes = 8 * 1024

// JobFunc runs the protocol for one job on an already-started daemon.
type JobFunc func(ctx context.Context, stdin io.Writer, stdout *bufio.Reader) error

// Process is ONE long-lived worker process and the one-job-at-a-time mutex in
// front of it: the daemon holds a single browser profile, and concurrent turns
// on one provider account risk interleaved output and CAPTCHA/lockout. A Pool
// owns several of these as isolated slots; each still runs exactly one job at a
// time.
type Process struct {
	Python string
	Script string
	// Env builds the worker's environment; nil means FilterEnv(nil), the local
	// allowlist. A Helper Node passes its own (no remote-browser endpoint).
	Env func() []string
	// NewCommand builds the daemon process. Overridable so tests can re-exec the
	// test binary as a fake daemon instead of depending on a Python interpreter.
	NewCommand func() *exec.Cmd

	// SlotMode, SlotID and PoolSize are set only by Pool. A slot daemon is spawned
	// with UBAG_WORKER_SLOT_ID / UBAG_WORKER_POOL_SIZE so the worker scopes its page
	// registry per slot, takes the cross-process identity lock and reaps the
	// registries of slots beyond the pool size (P3.3). The zero value is the single
	// legacy daemon.
	SlotMode bool
	SlotID   int
	PoolSize int

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	// stdoutFile is our read end of the daemon's stdout. Pipes are created
	// explicitly rather than via cmd.StdoutPipe() because Cmd.Wait() closes the
	// pipes it creates: the reaper goroutine below would then be free to close
	// stdout the instant the daemon exits, truncating a report mid-read. Owning
	// the fd means only discard() closes it, after the read is done.
	stdoutFile *os.File
	// exited is closed by the reaper goroutine when the daemon process dies, so
	// ensure can tell a live daemon from a corpse. exec.Cmd cannot: it only
	// populates ProcessState once Wait() returns.
	exited chan struct{}
}

// BuildCommand returns the command that would start this daemon.
func (p *Process) BuildCommand() *exec.Cmd {
	if p.NewCommand != nil {
		return p.NewCommand()
	}
	python := strings.TrimSpace(p.Python)
	if python == "" {
		python = "python"
	}
	cmd := exec.Command(python, strings.TrimSpace(p.Script))
	// The scrubbed env of the per-job worker: the daemon is long-lived, so leaking
	// the gateway's environment into it would be worse, not better.
	if p.Env != nil {
		cmd.Env = p.Env()
	} else {
		cmd.Env = FilterEnv(nil)
	}
	if p.SlotMode {
		// Appended last: for duplicate keys the last entry wins, so a stray
		// UBAG_WORKER_SLOT_ID in the gateway's own environment cannot renumber a slot.
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("UBAG_WORKER_SLOT_ID=%d", p.SlotID),
			fmt.Sprintf("UBAG_WORKER_POOL_SIZE=%d", p.PoolSize),
		)
	}
	cmd.Stderr = &limitedBuffer{max: MaxStderrBytes}
	return cmd
}

// exitedLocked reports whether the daemon process has died. Callers must hold mu.
func (p *Process) exitedLocked() bool {
	if p.exited == nil {
		return true
	}
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

// ensureLocked starts the daemon if it is not already running. A daemon that has
// died is reaped and replaced, so one crash cannot wedge every future job.
func (p *Process) ensureLocked() error {
	if p.cmd != nil && !p.exitedLocked() {
		return nil
	}
	p.discardLocked()

	if p.NewCommand == nil && strings.TrimSpace(p.Script) == "" {
		return fmt.Errorf("worker daemon script is not configured")
	}

	cmd := p.BuildCommand()
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		inRead.Close()
		inWrite.Close()
		return err
	}
	cmd.Stdin = inRead
	cmd.Stdout = outWrite

	if err := cmd.Start(); err != nil {
		inRead.Close()
		inWrite.Close()
		outRead.Close()
		outWrite.Close()
		return fmt.Errorf("start worker daemon: %w", err)
	}
	// The child owns its ends now. Dropping ours matters for stdout: otherwise
	// the read end never sees EOF when the daemon dies, and a job would block
	// forever instead of failing.
	inRead.Close()
	outWrite.Close()

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	p.cmd = cmd
	p.stdin = inWrite
	p.stdoutFile = outRead
	p.stdout = bufio.NewReaderSize(outRead, 64*1024)
	p.exited = exited
	if p.SlotMode {
		slog.Info("worker daemon started", "pid", cmd.Process.Pid, "slot", p.SlotID)
	} else {
		slog.Info("worker daemon started", "pid", cmd.Process.Pid)
	}
	return nil
}

// discardLocked tears the daemon down so the NEXT job starts a fresh one. Called
// whenever a job did not end cleanly: the daemon's warm page may hold a
// half-rendered turn, and reusing it could bleed one job's output into the next.
func (p *Process) discardLocked() {
	if p.cmd == nil {
		return
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	// The reaper goroutine owns Wait(); calling it here too would race it.
	if p.exited != nil {
		<-p.exited
	}
	if p.stdoutFile != nil {
		_ = p.stdoutFile.Close()
	}
	p.cmd, p.stdin, p.stdout, p.stdoutFile, p.exited = nil, nil, nil, nil, nil
}

// Run takes the one-job mutex and runs job on the daemon, spawning it lazily.
// The mutex is not context-aware, so a Pool reserves the slot first and only then
// calls this: the lock is then uncontended and never parks a cancelled caller.
//
// The run timeout starts HERE, after the daemon is held: a job queued behind
// another one must not spend its own maxRuntime budget waiting.
func (p *Process) Run(ctx context.Context, maxRuntime time.Duration, job JobFunc) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	runCtx, cancel := context.WithTimeout(ctx, maxRuntime)
	defer cancel()

	if err := runCtx.Err(); err != nil {
		return err
	}
	if err := p.ensureLocked(); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	stdin, stdout := p.stdin, p.stdout
	go func() { errCh <- job(runCtx, stdin, stdout) }()

	var err error
	select {
	case err = <-errCh:
	case <-runCtx.Done():
		// Cancel, caller deadline or maxRuntime: kill THIS daemon only. A pool's
		// other slots are separate processes and keep their warm pages.
		p.discardLocked()
		<-errCh
		return runCtx.Err()
	}
	if err != nil {
		// The daemon is now of unknown state (dead, mid-line, or holding a
		// half-finished page). Replace it rather than hand it the next job.
		p.discardLocked()
		return err
	}
	return nil
}

// Close shuts the daemon down (gateway shutdown).
func (p *Process) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discardLocked()
}

// Drain shuts the daemon down gracefully: once the active job is done (mu is
// held only by a running job) it closes the daemon's stdin, which ends the
// worker's serve loop on EOF so it closes its warm pages itself, and waits up to
// grace for the process to exit before falling back to a kill.
func (p *Process) Drain(grace time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil {
		return
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.exited != nil && grace > 0 {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-p.exited:
		case <-timer.C:
		}
	}
	p.discardLocked()
}

// KillForTest kills the daemon process without clearing the handles, so a test
// can assert the next job restarts it the way a real crash would.
func (p *Process) KillForTest() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		<-p.exited // deterministic: the runner must observe a corpse, not a race
	}
}

// limitedBuffer keeps the first max bytes written and drops the rest.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(input []byte) (int, error) {
	if remaining := b.max - b.buf.Len(); remaining > 0 {
		_, _ = b.buf.Write(input[:min(len(input), remaining)])
	}
	return len(input), nil
}
