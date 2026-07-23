package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// daemonJobEndKey marks the terminal control line of a job. It mirrors JOB_END
// in ubag_worker/live/daemon_protocol.py -- keep the two in sync.
const daemonJobEndKey = "__ubag_job_end__"

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
// this mutex is the Go half of the same invariant.
type DaemonWorkerRunner struct {
	Python     string
	Script     string
	MaxRuntime time.Duration
	Artifacts  artifacts.ArtifactStore

	// newCommand builds the daemon process. Overridable so tests can re-exec the
	// test binary as a fake daemon instead of depending on a Python interpreter.
	newCommand func() *exec.Cmd

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	// stdoutFile is our read end of the daemon's stdout. Pipes are created
	// explicitly rather than via cmd.StdoutPipe() because Cmd.Wait() closes the
	// pipes it creates: the reaper goroutine below would then be free to close
	// stdout the instant the daemon exits, truncating a report mid-read. Owning
	// the fd means only discardDaemon() closes it, after the read is done.
	stdoutFile *os.File
	// exited is closed by the reaper goroutine when the daemon process dies, so
	// ensureDaemon can tell a live daemon from a corpse. exec.Cmd cannot: it only
	// populates ProcessState once Wait() returns.
	exited chan struct{}
}

type daemonJobEnd struct {
	End    bool   `json:"__ubag_job_end__"`
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

type daemonJobRequest struct {
	JobID     string           `json:"job_id"`
	DeadlineS float64          `json:"deadline_s"`
	Payload   DispatchEnvelope `json:"payload"`
}

// runDaemonJob is the whole protocol: one request line out, then events in until
// the terminal marker. Split out from process management so it is testable
// without spawning anything.
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
	request, err := json.Marshal(daemonJobRequest{
		JobID:     envelope.JobID,
		DeadlineS: maxRuntime.Seconds(),
		Payload:   envelope,
	})
	if err != nil {
		return nil, err
	}
	if _, err := stdin.Write(append(request, '\n')); err != nil {
		return nil, fmt.Errorf("worker daemon stdin: %w", err)
	}

	var (
		body      bytes.Buffer
		bytesRead int
	)
	for {
		line, err := stdout.ReadString('\n')
		if err != nil {
			if len(strings.TrimSpace(line)) == 0 {
				return nil, fmt.Errorf("worker daemon ended without a terminal marker: %w", err)
			}
			// A final unterminated line cannot be trusted to be whole.
			return nil, fmt.Errorf("worker daemon output truncated mid-line")
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if end, ok := parseDaemonJobEnd(trimmed); ok {
			if end.Status != "completed" {
				if strings.TrimSpace(end.Error) != "" {
					return nil, fmt.Errorf("worker daemon job failed: %s", end.Error)
				}
				return nil, fmt.Errorf("worker daemon job failed")
			}
			return parseWorkerJSONL(body.Bytes())
		}

		bytesRead += len(line)
		if bytesRead > maxWorkerOutputBytes {
			return nil, fmt.Errorf("worker daemon stdout exceeded %d bytes", maxWorkerOutputBytes)
		}
		body.WriteString(trimmed)
		body.WriteByte('\n')
	}
}

// parseDaemonJobEnd reports whether a line is the terminal control marker. The
// marker carries no "type", so it would fail parseWorkerJSONL's validation if it
// were ever treated as an event -- it must be consumed as control.
func parseDaemonJobEnd(line string) (daemonJobEnd, bool) {
	if !strings.Contains(line, daemonJobEndKey) {
		return daemonJobEnd{}, false
	}
	var end daemonJobEnd
	if err := json.Unmarshal([]byte(line), &end); err != nil || !end.End {
		return daemonJobEnd{}, false
	}
	return end, true
}

func (r *DaemonWorkerRunner) buildCommand() *exec.Cmd {
	if r.newCommand != nil {
		return r.newCommand()
	}
	python := strings.TrimSpace(r.Python)
	if python == "" {
		python = "python"
	}
	cmd := exec.Command(python, strings.TrimSpace(r.Script))
	// Same scrubbed env as the per-job worker: the daemon is long-lived, so
	// leaking the gateway's environment into it would be worse, not better.
	cmd.Env = minimalWorkerEnv()
	cmd.Stderr = &limitedBuffer{max: maxWorkerStderrBytes}
	return cmd
}

// daemonExited reports whether the daemon process has died. Callers must hold mu.
func (r *DaemonWorkerRunner) daemonExited() bool {
	if r.exited == nil {
		return true
	}
	select {
	case <-r.exited:
		return true
	default:
		return false
	}
}

// ensureDaemon starts the daemon if it is not already running. Callers must hold
// mu. A daemon that has died is reaped and replaced, so one crash cannot wedge
// every future job.
func (r *DaemonWorkerRunner) ensureDaemon() error {
	if r.cmd != nil && !r.daemonExited() {
		return nil
	}
	r.discardDaemon()

	if r.newCommand == nil && strings.TrimSpace(r.Script) == "" {
		return fmt.Errorf("worker daemon script is not configured")
	}

	cmd := r.buildCommand()
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

	r.cmd = cmd
	r.stdin = inWrite
	r.stdoutFile = outRead
	r.stdout = bufio.NewReaderSize(outRead, 64*1024)
	r.exited = exited
	slog.Info("worker daemon started", "pid", cmd.Process.Pid)
	return nil
}

// discardDaemon tears the daemon down so the NEXT job starts a fresh one. Called
// whenever a job did not end cleanly: the daemon's warm page may hold a
// half-rendered turn, and reusing it could bleed one job's output into the next.
// Callers must hold mu.
func (r *DaemonWorkerRunner) discardDaemon() {
	if r.cmd == nil {
		return
	}
	if r.stdin != nil {
		_ = r.stdin.Close()
	}
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	// The reaper goroutine owns Wait(); calling it here too would race it.
	if r.exited != nil {
		<-r.exited
	}
	if r.stdoutFile != nil {
		_ = r.stdoutFile.Close()
	}
	r.cmd, r.stdin, r.stdout, r.stdoutFile, r.exited = nil, nil, nil, nil, nil
}

// RunWorker implements WorkerRunner.
func (r *DaemonWorkerRunner) RunWorker(
	ctx context.Context, envelope DispatchEnvelope,
) ([]jobs.WorkerEvent, error) {
	maxRuntime := r.MaxRuntime
	if maxRuntime <= 0 {
		maxRuntime = defaultWorkerMaxRuntime
	}

	// Materialize any declared attachments exactly as the per-job runner does, so
	// attachment jobs behave identically under the daemon.
	cleanupAttachments, err := ProcessWorkerRunner{Artifacts: r.Artifacts}.
		materializeAttachments(ctx, &envelope)
	if err != nil {
		return nil, err
	}
	if cleanupAttachments != nil {
		defer cleanupAttachments()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.ensureDaemon(); err != nil {
		return nil, err
	}

	events, err := runDaemonJob(r.stdin, r.stdout, envelope, maxRuntime)
	if err != nil {
		// The daemon is now of unknown state (dead, mid-line, or holding a
		// half-finished page). Replace it rather than hand it the next job.
		r.discardDaemon()
		return nil, err
	}
	return events, nil
}

// Close shuts the daemon down (gateway shutdown).
func (r *DaemonWorkerRunner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discardDaemon()
}

// killForTest kills the daemon process without clearing the runner's handles, so
// a test can assert the next job restarts it the way a real crash would.
func (r *DaemonWorkerRunner) killForTest() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
		<-r.exited // deterministic: the runner must observe a corpse, not a race
	}
}
