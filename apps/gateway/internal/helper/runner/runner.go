// Package runner is the real helper.Runner of a Helper Node (P4.12): it runs one
// attempt on a bounded pool of isolated warm worker processes.
//
// It is the P3.6 pool, not a new mutex: the placement machinery (one active job
// per physical browser session, warm affinity, bounded wait, drain) is
// workerdaemon.Pool, the same code the primary's executor.DaemonPool runs. This
// package adds only what is specific to the helper side of the trust plane:
//
//   - the worker payload is built from the helper's validated AttemptSpec, never
//     from a primary envelope: the browser profile is addressed only by the
//     opaque identity_ref (the worker derives the directory from it in
//     UBAG_HELPER_PLANE mode), and every caller-supplied profile option and
//     primary-local path is dropped (the twin of executor.NewHelperAttemptSpec);
//   - the worker environment is the shared allowlist minus the primary's remote
//     browser and noVNC endpoints (the twin of executor.helperWorkerEnv);
//   - declared attachments are staged from an AssetSource into ubag-attach- temp
//     files and verified (size and SHA-256) before the worker sees a path;
//   - the worker's JSONL events are mapped onto helper attempt events, and its
//     terminal event onto an AttemptOutcome obeying decision D4 (a deadline cut
//     is timed_out with partial and no result; the worker's own error text never
//     leaves the node).
//
// It imports only the standard library, the helper core, the pool and the
// attachment manifest parser, never internal/executor.
package runner

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/attachments"
	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/workerdaemon"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

const (
	// defaultDeadlineSlack: the worker's own strict stream end emits timed_out with
	// the partial text; it must stay below the service's backstop
	// (helper.Config.DeadlineGrace, 15s by default).
	defaultDeadlineSlack = 10 * time.Second

	maxOutputBytes   = 1 << 20  // one attempt's worker stdout, as the primary's per-job runner
	maxPartialBytes  = 32 << 10 // partial text carried on a timed_out terminal
	maxReasonRunes   = 64
	maxMessageRunes  = 512
	workerPlaneEnv   = "UBAG_HELPER_PLANE"
	workerProfileEnv = "UBAG_PROFILE_DIR"
)

var (
	// profileRefRe is the worker's own pattern for a helper profile_ref
	// (_HELPER_PROFILE_REF_RE in envelope.py): no path characters. The helper
	// core's identity_ref pattern is wider (it also allows ':'), so an identity
	// the worker would refuse is refused here, before a daemon is involved.
	profileRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	tokenRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	errorCodeRe  = regexp.MustCompile(`^UBAG-[A-Z0-9-]{3,60}$`)

	// The Helper Node never learns where the primary's browser or viewer lives.
	deniedEnv = map[string]struct{}{
		"UBAG_REMOTE_BROWSER_ENDPOINT": {},
		"UBAG_NOVNC_BASE_URL":          {},
	}
	// Local-only job options: caller-chosen profile directories and the caller's
	// identity label. On a helper the identity is the primary-issued identity_ref only.
	droppedOptions = []string{"user_data_dir", "profile_dir", "profile_path", "account_binding_id"}
	// Primary-local input fields injected by attachment materialization.
	droppedInputs = []string{"attachment_local_paths", "audio_local_path"}
)

// AssetSource yields the bytes of one declared attachment of a job (in
// production: the P4.10 staging client pulling from the primary under an attempt
// token). The runner verifies size and SHA-256 itself, so a source is not trusted
// to have done it.
type AssetSource interface {
	Open(ctx context.Context, jobID string, asset *helperv1.Asset) (io.ReadCloser, error)
}

// Config configures a Runner.
type Config struct {
	// Python and Script start the warm worker daemon (apps/worker/run_worker_daemon.py).
	Python string
	Script string
	// Slots is the number of worker processes; <= 0 means 1 (a new helper starts at
	// one workload). Keep it equal to helper.Config.MaxAttempts: the service admits
	// at most that many attempts, so the pool never has to refuse one.
	Slots int
	// ProfileRoot is the directory profiles live under on this node
	// (UBAG_PROFILE_DIR for the workers); "" keeps the worker's default.
	ProfileRoot string
	// Assets stages declared attachments; nil refuses attempts that carry any.
	Assets AssetSource
	// MaxWait bounds how long a job waits for a slot (default 30s).
	MaxWait time.Duration
	// DeadlineSlack is how long past the attempt deadline the worker gets to end
	// the attempt itself before its daemon is killed (default 10s). Keep it below
	// helper.Config.DeadlineGrace.
	DeadlineSlack time.Duration
	// NewSlotCommand builds a slot's process; tests re-exec the test binary.
	NewSlotCommand func(slot int) *exec.Cmd
}

// Runner implements helper.Runner over a workerdaemon.Pool.
type Runner struct {
	cfg  Config
	pool *workerdaemon.Pool
}

var _ helper.Runner = (*Runner)(nil)

// New builds the pool. Nothing is started until the first attempt.
func New(cfg Config) (*Runner, error) {
	if cfg.NewSlotCommand == nil && strings.TrimSpace(cfg.Script) == "" {
		return nil, errors.New("helper runner: the worker daemon script is not configured")
	}
	if cfg.Slots <= 0 {
		cfg.Slots = 1
	}
	if cfg.DeadlineSlack <= 0 {
		cfg.DeadlineSlack = defaultDeadlineSlack
	}
	if cfg.Slots > workerdaemon.MaxPoolSize {
		return nil, fmt.Errorf("helper runner: at most %d slots", workerdaemon.MaxPoolSize)
	}
	return &Runner{cfg: cfg, pool: &workerdaemon.Pool{
		Python: cfg.Python, Script: cfg.Script, Env: func() []string { return WorkerEnv(cfg.ProfileRoot) },
		Size: cfg.Slots, MaxWait: cfg.MaxWait, MaxQueue: cfg.Slots, NewSlotCommand: cfg.NewSlotCommand,
	}}, nil
}

// Close drains the pool: running attempts finish (the service cancels them first
// on shutdown), then each worker is asked to exit and killed after a grace.
func (r *Runner) Close() { r.pool.Close() }

// WorkerEnv is the environment of a helper-side worker: the shared allowlist minus
// the primary's remote-browser and noVNC endpoints, the helper-mode switch (set by
// the helper, never by a job) and the node's profile root.
func WorkerEnv(profileRoot string) []string {
	env := append(workerdaemon.FilterEnv(deniedEnv), workerPlaneEnv+"=1")
	if root := strings.TrimSpace(profileRoot); root != "" {
		env = append(env, workerProfileEnv+"="+root)
	}
	return env
}

// Run implements helper.Runner.
func (r *Runner) Run(ctx context.Context, spec helper.AttemptSpec, emit helper.EmitFunc) error {
	if !profileRefRe.MatchString(spec.IdentityRef) {
		return terminalFailed(emit, "helper_identity_ref_unsupported", "identity_ref", false)
	}
	input, options, err := scrubbed(spec)
	if err != nil {
		return terminalFailed(emit, "helper_input_invalid", "invalid_input", false)
	}
	run := &attemptRun{spec: spec, emit: emit}
	// The pool's gate key is tenant-free and per physical session: the same
	// identity_ref is the same profile directory on this node. The worker's flock
	// (slot mode) is the cross-process backstop.
	key := workerdaemon.IdentityKey("helper:"+spec.IdentityRef, spec.Target)
	maxRuntime := spec.Deadline + r.cfg.DeadlineSlack

	err = r.pool.Run(ctx, key, func(slot *workerdaemon.Slot) error {
		if err := emit(helper.Event{Type: helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_STARTED}); err != nil {
			return err
		}
		cleanup, err := r.stage(ctx, spec, input)
		if err != nil {
			return err
		}
		defer cleanup()
		payload := map[string]any{
			"job_id":      spec.JobID,
			"trace_id":    spec.TraceID,
			"profile_ref": spec.IdentityRef,
			"attempt":     map[string]any{"id": spec.AttemptID},
			"job": map[string]any{
				"target": spec.Target, "command_type": spec.CommandType, "input": input, "options": options,
			},
		}
		return slot.Run(ctx, maxRuntime, func(runCtx context.Context, stdin io.Writer, stdout *bufio.Reader) error {
			return workerdaemon.ReadJob(stdin, stdout, workerdaemon.Request{
				JobID: spec.JobID, DeadlineS: spec.Deadline.Seconds(), Payload: payload,
			}, maxOutputBytes, nil, run.onLine)
		})
	})
	switch {
	case err == nil:
		return nil // the worker ended cleanly; a missing terminal is the service's to write
	case ctx.Err() != nil:
		return err // cancel, lease expiry, deadline backstop, drain or shutdown: the service knows the cause
	}
	var staging *stageError
	switch {
	case errors.As(err, &staging):
		return terminalFailed(emit, staging.code, staging.reason, false)
	case errors.Is(err, workerdaemon.ErrOverloaded), errors.Is(err, workerdaemon.ErrClosed):
		return terminalFailed(emit, "helper_pool_unavailable", "pool_unavailable", false)
	case errors.Is(err, context.DeadlineExceeded):
		// The worker overran deadline + slack without ending the attempt: a
		// deadline cut (D4), with partial output flagged and never a result.
		return emit(helper.Event{
			Type: helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL,
			Outcome: &helperv1.AttemptOutcome{
				Status: helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT, StreamEndReason: "deadline",
				Partial: run.tokens > 0, Submitted: run.submitted,
			},
		})
	}
	return err // the worker died or broke protocol: the service ends the attempt failed (runner_error)
}

// stageError is a failure before the worker was touched; it is typed so the
// attempt ends with a stable code instead of an error text.
type stageError struct{ code, reason string }

func (e *stageError) Error() string { return "helper: staging failed: " + e.code }

// terminalFailed ends the attempt failed with a stable code and no worker text.
func terminalFailed(emit helper.EmitFunc, code, reason string, submitted bool) error {
	return emit(helper.Event{
		Type: helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL,
		Outcome: &helperv1.AttemptOutcome{
			Status: helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED, StreamEndReason: reason, ErrorCode: code,
			ErrorMessage: "the attempt could not be run on this node", Submitted: submitted,
		},
	})
}

// scrubbed parses the spec's JSON objects and drops the fields a helper must not
// honour (see droppedOptions / droppedInputs).
func scrubbed(spec helper.AttemptSpec) (input, options map[string]any, err error) {
	if err = json.Unmarshal([]byte(spec.InputJSON), &input); err != nil || input == nil {
		return nil, nil, errors.New("input_json is not an object")
	}
	options = map[string]any{}
	if strings.TrimSpace(spec.OptionsJSON) != "" {
		if err = json.Unmarshal([]byte(spec.OptionsJSON), &options); err != nil || options == nil {
			return nil, nil, errors.New("options_json is not an object")
		}
	}
	for _, k := range droppedInputs {
		delete(input, k)
	}
	for _, k := range droppedOptions {
		delete(options, k)
	}
	return input, options, nil
}

// stage materializes the declared attachments into ubag-attach- temp files (the
// prefix the worker's path guard requires), verifying each asset's size and
// SHA-256 as it is written, and points the worker at them. It fails closed: the
// asset list and the declared manifest must match one to one, and any mismatch
// removes everything already written.
func (r *Runner) stage(ctx context.Context, spec helper.AttemptSpec, input map[string]any) (func(), error) {
	declared, err := attachments.DeclaredAttachments(input)
	if err != nil {
		return nil, &stageError{"helper_assets_invalid", "assets_invalid"}
	}
	if len(declared) == 0 && len(spec.Assets) == 0 {
		return func() {}, nil
	}
	if len(declared) != len(spec.Assets) {
		return nil, &stageError{"helper_assets_invalid", "assets_invalid"}
	}
	if r.cfg.Assets == nil {
		return nil, &stageError{"helper_assets_unavailable", "assets_unavailable"}
	}
	// Asset.name is the declared attachment key (assumption recorded in the P4.12
	// shard: the contract's name field carries it, the helper pulls by it).
	byKey := make(map[string]*helperv1.Asset, len(spec.Assets))
	for _, a := range spec.Assets {
		if _, dup := byKey[a.GetName()]; dup {
			return nil, &stageError{"helper_assets_invalid", "assets_invalid"}
		}
		byKey[a.GetName()] = a
	}

	var files, dirs []string
	cleanup := func() {
		for _, f := range files {
			_ = os.Remove(f)
		}
		for _, d := range dirs {
			_ = os.Remove(d)
		}
	}
	paths := make([]any, 0, len(declared))
	byAttachment := make(map[string]string, len(declared))
	for _, att := range declared {
		asset := byKey[att.Key]
		if asset == nil {
			cleanup()
			return nil, &stageError{"helper_assets_invalid", "assets_invalid"}
		}
		dir, err := os.MkdirTemp("", "ubag-attach-*")
		if err != nil {
			cleanup()
			return nil, &stageError{"helper_assets_unavailable", "assets_unavailable"}
		}
		dirs = append(dirs, dir)
		dst := filepath.Join(dir, attachments.MaterializedFilename(att, att.ContentType))
		files = append(files, dst)
		if err := r.fetch(ctx, spec.JobID, asset, dst); err != nil {
			cleanup()
			if errors.Is(err, errIntegrity) {
				return nil, &stageError{"helper_asset_integrity", "asset_integrity"}
			}
			return nil, &stageError{"helper_assets_unavailable", "assets_unavailable"}
		}
		paths = append(paths, dst)
		byAttachment[att.Key] = dst
	}
	input["attachment_local_paths"] = paths
	if audioKey, ok := input["audio_artifact_key"].(string); ok {
		if p, ok := byAttachment[strings.TrimSpace(audioKey)]; ok {
			input["audio_local_path"] = p
		}
	}
	return cleanup, nil
}

var errIntegrity = errors.New("asset failed integrity verification")

// fetch writes one asset to dst and verifies its size and SHA-256 against the
// manifest the primary sent, reading at most size+1 bytes so an overlong stream
// is caught without buffering it.
func (r *Runner) fetch(ctx context.Context, jobID string, asset *helperv1.Asset, dst string) error {
	rc, err := r.cfg.Assets.Open(ctx, jobID, asset)
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(rc, asset.GetSizeBytes()+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return copyErr
	case closeErr != nil:
		return closeErr
	case n != asset.GetSizeBytes(), hex.EncodeToString(h.Sum(nil)) != asset.GetSha256():
		return errIntegrity
	}
	return nil
}
