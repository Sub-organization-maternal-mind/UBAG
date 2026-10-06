// Package workerdaemon is the warm-worker-process machinery shared by the
// primary's executor (DaemonWorkerRunner, DaemonPool) and the Helper Node
// (internal/helper/runner): the stdio job protocol, one supervised daemon process
// (Process), the identity-gated bounded pool of them (Pool) and the worker
// environment allowlist.
//
// It is deliberately a leaf: standard library only. internal/executor links the
// object-store, queue, Postgres and plugin stacks, which the ubag-helper binary
// must not (P4.11 measured 228 vs 114 packages), so everything the helper needs
// from the executor's P3.6 pool lives here and the executor is a thin adapter.
package workerdaemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// JobEndKey marks the terminal control line of a job. It mirrors JOB_END in
// ubag_worker/live/daemon_protocol.py -- keep the two in sync.
const JobEndKey = "__ubag_job_end__"

// Request is the one stdin line that starts a job (protocol v1).
type Request struct {
	JobID     string  `json:"job_id"`
	DeadlineS float64 `json:"deadline_s"`
	Payload   any     `json:"payload"`
}

// JobEnd is the terminal control line of a job.
type JobEnd struct {
	End    bool   `json:"__ubag_job_end__"`
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// ReadJob is the protocol core: one request line out, then lines in (each handed
// to onLine, trimmed and non-empty) until the terminal marker. observe, when
// non-nil, sees every non-control line first (before the output bound), so a
// caller can notice the submission boundary even on a stream that then fails.
//
// A stream that ends without a marker is an error, never a success: the daemon
// died mid-job, and returning the events collected so far would hand back a
// TRUNCATED report as though it were complete. Output over maxBytes is an error,
// never a truncation.
func ReadJob(
	stdin io.Writer,
	stdout *bufio.Reader,
	req Request,
	maxBytes int,
	observe func(line string),
	onLine func(line string) error,
) error {
	request, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := stdin.Write(append(request, '\n')); err != nil {
		return fmt.Errorf("worker daemon stdin: %w", err)
	}

	bytesRead := 0
	for {
		line, err := ReadBoundedLine(stdout, maxBytes)
		if err != nil {
			return fmt.Errorf("worker daemon ended without a terminal marker: %w", err)
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if end, ok := ParseJobEnd(trimmed); ok {
			if end.JobID != req.JobID {
				return fmt.Errorf(
					"worker daemon terminal marker job_id %q does not match active job %q",
					end.JobID,
					req.JobID,
				)
			}
			if end.Status != "completed" {
				if strings.TrimSpace(end.Error) != "" {
					return fmt.Errorf("worker daemon job failed: %s", end.Error)
				}
				return fmt.Errorf("worker daemon job failed")
			}
			return nil
		}

		if observe != nil {
			observe(trimmed)
		}

		bytesRead += len(line) + 1
		if bytesRead > maxBytes {
			return fmt.Errorf("worker daemon stdout exceeded %d bytes", maxBytes)
		}
		if err := onLine(trimmed); err != nil {
			return err
		}
	}
}

// ReadBoundedLine reads one line of at most limit bytes.
func ReadBoundedLine(reader *bufio.Reader, limit int) (string, error) {
	if limit <= 0 {
		return "", fmt.Errorf("worker daemon stdout line limit is invalid")
	}
	var line bytes.Buffer
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if err != nil {
			if line.Len() > 0 || len(fragment) > 0 {
				return "", fmt.Errorf("worker daemon output truncated mid-line")
			}
			return "", err
		}
		if line.Len()+len(fragment) > limit {
			return "", fmt.Errorf("worker daemon stdout line exceeded %d bytes", limit)
		}
		_, _ = line.Write(fragment)
		if !isPrefix {
			return line.String(), nil
		}
	}
}

// ParseJobEnd reports whether a line is the terminal control marker. The marker
// carries no "type", so it would fail an event parser's validation if it were
// ever treated as an event -- it must be consumed as control.
func ParseJobEnd(line string) (JobEnd, bool) {
	if !strings.Contains(line, JobEndKey) {
		return JobEnd{}, false
	}
	var end JobEnd
	if err := json.Unmarshal([]byte(line), &end); err != nil || !end.End {
		return JobEnd{}, false
	}
	return end, true
}
