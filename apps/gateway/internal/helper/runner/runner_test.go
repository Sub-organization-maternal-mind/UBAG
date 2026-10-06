package runner

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helper"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---- fake worker daemon ----------------------------------------------------
//
// The test binary re-executes itself as the warm worker daemon (the same trick
// the executor's pool tests use), so the whole path runs for real: process
// spawn, the stdio protocol, event mapping and terminal outcomes.

func TestHelperDaemon(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_DAEMON") != "1" {
		return
	}
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			os.Exit(0) // stdin EOF: the pool is draining us
		}
		var req struct {
			JobID   string         `json:"job_id"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(line, &req) != nil {
			os.Exit(2)
		}
		serveJob(req.JobID, req.Payload)
	}
}

func say(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func ev(typ string, data map[string]any) { say(map[string]any{"type": typ, "data": data}) }

func serveJob(jobID string, payload map[string]any) {
	job, _ := payload["job"].(map[string]any)
	input, _ := job["input"].(map[string]any)
	mode, _ := input["mode"].(string)
	ev("queued", map[string]any{"status": "queued"})
	ev("session.opening", map[string]any{"status": "opening"})
	switch mode {
	case "work":
		ev("prompt_submitted", map[string]any{"status": "prompt_submitted"})
		start := time.Now()
		ms, _ := input["sleep_ms"].(float64)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		out, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "start": start.UnixMicro(), "end": time.Now().UnixMicro()})
		ev("completed", map[string]any{"result": map[string]any{"type": "text", "text": string(out)}})
	case "blocked":
		ev("session.manual_action_required", map[string]any{"reason": "login_required", "message": "WORKER-FREE-TEXT-SECRET", "novnc_url": "http://primary/x"})
		ev("blocked", map[string]any{"reason": "login_required", "message": "WORKER-FREE-TEXT-SECRET", "error_code": "UBAG-ADAPTER-DRIFT-014"})
	case "failed":
		ev("prompt_submitted", map[string]any{})
		ev("failed", map[string]any{"message": "WORKER-FREE-TEXT-SECRET"})
	case "timed_out":
		ev("prompt_submitted", map[string]any{})
		ev("token", map[string]any{"token_index": 0, "delta": map[string]any{"text": "par"}})
		ev("timed_out", map[string]any{
			"stream_end_reason": "deadline", "submitted": true,
			"partial": map[string]any{"text": "partial text", "token_events": 1},
		})
	case "hang":
		ev("prompt_submitted", map[string]any{})
		time.Sleep(time.Hour)
	case "die":
		os.Exit(3)
	case "payload":
		report := map[string]any{
			"profile_ref": payload["profile_ref"], "options": job["options"], "has_tenant": payload["tenant_id"] != nil,
			"has_callbacks": job["callbacks"] != nil || job["context"] != nil, "attempt": payload["attempt"],
			"target": job["target"], "env_plane": os.Getenv("UBAG_HELPER_PLANE"),
		}
		if paths, ok := input["attachment_local_paths"].([]any); ok {
			var files []map[string]any
			for _, p := range paths {
				path, _ := p.(string)
				body, err := os.ReadFile(path)
				files = append(files, map[string]any{"path": path, "body": string(body), "err": fmt.Sprint(err)})
			}
			report["files"] = files
		}
		if v, ok := input["audio_local_path"]; ok {
			report["audio_local_path"] = v
		}
		out, _ := json.Marshal(report)
		ev("completed", map[string]any{"result": map[string]any{"type": "text", "text": string(out)}})
	default:
		ev("prompt_submitted", map[string]any{})
		ev("token", map[string]any{"token_index": 0, "delta": map[string]any{"text": "he"}})
		ev("token", map[string]any{"token_index": 1, "delta": map[string]any{"text": "llo"}})
		ev("completed", map[string]any{"result": map[string]any{"type": "text", "text": "hello"}})
		// Telemetry after the terminal: must not reach the primary.
		ev("conversation.thread_bound", map[string]any{"thread_ref": "https://x/y"})
	}
	say(map[string]any{"__ubag_job_end__": true, "job_id": jobID, "status": "completed"})
}

// ---- harness ---------------------------------------------------------------

type fakeAssets struct {
	mu     sync.Mutex
	data   map[string][]byte
	opened []string
	open   func(name string) (io.ReadCloser, error) // overrides data when set
}

func (f *fakeAssets) Open(_ context.Context, _ string, a *helperv1.Asset) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opened = append(f.opened, a.GetName())
	f.mu.Unlock()
	if f.open != nil {
		return f.open(a.GetName())
	}
	b, ok := f.data[a.GetName()]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

type rig struct {
	r       *Runner
	spawned atomic.Int32
}

func newRig(t *testing.T, slots int, tune func(*Config)) *rig {
	t.Helper()
	g := &rig{}
	cfg := Config{
		Slots: slots, MaxWait: 10 * time.Second, DeadlineSlack: 200 * time.Millisecond,
		NewSlotCommand: func(slot int) *exec.Cmd {
			g.spawned.Add(1)
			cmd := exec.Command(os.Args[0], "-test.run=TestHelperDaemon")
			cmd.Env = append(os.Environ(), "GO_WANT_HELPER_DAEMON=1", "GO_HELPER_SLOT="+strconv.Itoa(slot))
			return cmd
		},
	}
	if tune != nil {
		tune(&cfg)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g.r = r
	t.Cleanup(r.Close)
	return g
}

type sink struct {
	mu     sync.Mutex
	events []helper.Event
}

func (s *sink) emit(e helper.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.events); n > 0 && s.events[n-1].Type == helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL {
		return errors.New("attempt ended")
	}
	s.events = append(s.events, e)
	return nil
}

func (s *sink) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		out = append(out, strings.TrimPrefix(e.Type.String(), "ATTEMPT_EVENT_TYPE_"))
	}
	return out
}

func (s *sink) outcome(t *testing.T) *helperv1.AttemptOutcome {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 || s.events[len(s.events)-1].Outcome == nil {
		t.Fatalf("no terminal event: %v", s.events)
	}
	return s.events[len(s.events)-1].Outcome
}

func spec(job, ident, input string) helper.AttemptSpec {
	return helper.AttemptSpec{
		JobID: job, AttemptID: "att_" + job, Generation: 1, Provider: "chatgpt_web", Target: "chatgpt_web", CommandType: "submit",
		IdentityRef: ident, InputJSON: input, OptionsJSON: `{}`, TraceID: "trace-1", Deadline: 20 * time.Second,
	}
}

func run(t *testing.T, r *Runner, s helper.AttemptSpec) (*sink, error) {
	t.Helper()
	sk := &sink{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := r.Run(ctx, s, sk.emit)
	return sk, err
}

// ---- tests -----------------------------------------------------------------

func TestRunMapsWorkerEventsAndDropsTelemetry(t *testing.T) {
	g := newRig(t, 1, nil)
	sk, err := run(t, g.r, spec("job1", "ident-1", `{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"STARTED", "BROWSER_OPENED", "PROMPT_SUBMITTED", "TOKEN", "TOKEN", "TERMINAL"}
	if got := sk.types(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v (queued and post-terminal telemetry are not forwarded)", got, want)
	}
	o := sk.outcome(t)
	if o.GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED || !strings.Contains(o.GetResultJson(), `"hello"`) || !o.GetSubmitted() {
		t.Fatalf("outcome = %v", o)
	}
}

func TestSlotStaysWarmAcrossJobsOfOneIdentity(t *testing.T) {
	g := newRig(t, 2, nil)
	for i := range 3 {
		if _, err := run(t, g.r, spec("jobw"+strconv.Itoa(i), "ident-w", `{"mode":"work","sleep_ms":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	if n := g.spawned.Load(); n != 1 {
		t.Fatalf("%d daemons started for 3 sequential jobs of one identity, want 1 (warm affinity)", n)
	}
}

func TestBlockedAndFailedKeepWorkerTextOnTheNode(t *testing.T) {
	g := newRig(t, 1, nil)
	sk, err := run(t, g.r, spec("jobb", "ident-b", `{"mode":"blocked"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := sk.types(); !slices.Contains(got, "MANUAL_ACTION_REQUIRED") || got[len(got)-1] != "TERMINAL" {
		t.Fatalf("events = %v", got)
	}
	o := sk.outcome(t)
	if o.GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED || o.GetErrorCode() != "UBAG-ADAPTER-DRIFT-014" || o.GetStreamEndReason() != "login_required" {
		t.Fatalf("outcome = %v", o)
	}

	sk, err = run(t, g.r, spec("jobf", "ident-b", `{"mode":"failed"}`))
	if err != nil {
		t.Fatal(err)
	}
	o = sk.outcome(t)
	if o.GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED || !o.GetSubmitted() || o.GetErrorCode() != "helper_worker_failed" {
		t.Fatalf("failed outcome = %v", o)
	}
	for _, e := range sk.events {
		if strings.Contains(e.DataJSON, "SECRET") || strings.Contains(e.Outcome.String(), "SECRET") {
			t.Fatalf("worker free text crossed to the primary: %v", e)
		}
	}
	for _, e := range sk.events {
		if e.Type == helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_MANUAL_ACTION_REQUIRED && (strings.Contains(e.DataJSON, "SECRET") || strings.Contains(e.DataJSON, "novnc")) {
			t.Fatalf("manual action event leaked: %q", e.DataJSON)
		}
	}
}

// D4: a deadline cut is timed_out with partial text on the data and never a result.
func TestWorkerDeadlineCutIsTimedOutWithPartialAndNoResult(t *testing.T) {
	g := newRig(t, 1, nil)
	sk, err := run(t, g.r, spec("jobt", "ident-t", `{"mode":"timed_out"}`))
	if err != nil {
		t.Fatal(err)
	}
	o := sk.outcome(t)
	if o.GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT || !o.GetPartial() || o.GetResultJson() != "" || o.GetStreamEndReason() != "deadline" || !o.GetSubmitted() {
		t.Fatalf("outcome = %v", o)
	}
	last := sk.events[len(sk.events)-1]
	if !strings.Contains(last.DataJSON, "partial text") {
		t.Fatalf("partial text missing from the terminal data: %q", last.DataJSON)
	}
}

func TestWorkerThatOverrunsTheDeadlineIsKilledAndTimedOut(t *testing.T) {
	g := newRig(t, 1, nil)
	s := spec("jobh", "ident-h", `{"mode":"hang"}`)
	s.Deadline = 200 * time.Millisecond
	sk, err := run(t, g.r, s)
	if err != nil {
		t.Fatal(err)
	}
	o := sk.outcome(t)
	if o.GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_TIMED_OUT || o.GetStreamEndReason() != "deadline" || o.GetResultJson() != "" || !o.GetSubmitted() {
		t.Fatalf("outcome = %v", o)
	}
	// The daemon was killed with the job: the next job gets a fresh one.
	before := g.spawned.Load()
	if _, err := run(t, g.r, spec("jobh2", "ident-h", `{}`)); err != nil {
		t.Fatal(err)
	}
	if g.spawned.Load() != before+1 {
		t.Fatal("a daemon that overran its deadline must be replaced, not reused")
	}
}

func TestWorkerCrashEndsWithoutATerminalSoTheServiceFailsTheAttempt(t *testing.T) {
	g := newRig(t, 1, nil)
	sk, err := run(t, g.r, spec("jobc", "ident-c", `{"mode":"die"}`))
	if err == nil {
		t.Fatalf("a dead worker must surface as an error; events %v", sk.types())
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatal("unexpected text")
	}
}

func TestCancelledContextKillsOnlyThatSlot(t *testing.T) {
	g := newRig(t, 2, nil)
	sk := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.r.Run(ctx, spec("jobx", "ident-x", `{"mode":"hang"}`), sk.emit) }()
	waitFor(t, func() bool { return slices.Contains(sk.types(), "PROMPT_SUBMITTED") })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (the service writes the cancel terminal)", err)
	}
	if _, err := run(t, g.r, spec("joby", "ident-y", `{}`)); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---- the identity lock -----------------------------------------------------

type window struct{ Pid, Start, End float64 }

func windowOf(t *testing.T, sk *sink) window {
	t.Helper()
	o := sk.outcome(t)
	var res struct{ Text string }
	if err := json.Unmarshal([]byte(o.GetResultJson()), &res); err != nil {
		t.Fatalf("result %q: %v", o.GetResultJson(), err)
	}
	var w window
	if err := json.Unmarshal([]byte(res.Text), &w); err != nil {
		t.Fatalf("window %q: %v", res.Text, err)
	}
	return w
}

// One active operation per physical provider session, whatever the slot count:
// the pool's gate serializes two attempts for one identity_ref even with free
// slots (the helper core's own gate normally refuses the second one earlier).
func TestPoolSerializesOneIdentityAndOverlapsDifferentOnes(t *testing.T) {
	g := newRig(t, 3, nil)
	runMany := func(idents ...string) []window {
		out := make([]window, len(idents))
		var wg sync.WaitGroup
		for i, id := range idents {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sk, err := run(t, g.r, spec("jobi"+strconv.Itoa(i)+id, id, `{"mode":"work","sleep_ms":300}`))
				if err != nil {
					t.Error(err)
					return
				}
				out[i] = windowOf(t, sk)
			}()
		}
		wg.Wait()
		return out
	}
	overlaps := func(a, b window) bool { return a.Start < b.End && b.Start < a.End }

	same := runMany("ident-same", "ident-same")
	if t.Failed() {
		return
	}
	if overlaps(same[0], same[1]) {
		t.Fatalf("two attempts on one identity overlapped: %+v %+v", same[0], same[1])
	}
	diff := runMany("ident-a", "ident-b")
	if t.Failed() {
		return
	}
	if !overlaps(diff[0], diff[1]) {
		t.Fatalf("two identities with free slots did not run in parallel: %+v %+v", diff[0], diff[1])
	}
	if diff[0].Pid == diff[1].Pid {
		t.Fatal("two parallel attempts shared one worker process")
	}
}

func TestSaturationIsABoundedTypedRefusalNotAFailureOfTheWorker(t *testing.T) {
	g := newRig(t, 1, func(c *Config) { c.MaxWait = 100 * time.Millisecond })
	first := &sink{}
	done := make(chan error, 1)
	go func() {
		done <- g.r.Run(context.Background(), spec("jobs1", "ident-s1", `{"mode":"work","sleep_ms":800}`), first.emit)
	}()
	waitFor(t, func() bool { return slices.Contains(first.types(), "STARTED") })

	sk, err := run(t, g.r, spec("jobs2", "ident-s2", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	o := sk.outcome(t)
	if o.GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED || o.GetErrorCode() != "helper_pool_unavailable" || o.GetSubmitted() {
		t.Fatalf("outcome = %v", o)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// ---- environment -----------------------------------------------------------

func TestWorkerEnvHoldsNoneOfTheForbiddenKeys(t *testing.T) {
	// Everything a primary carries (and the two endpoints the helper must never
	// learn) is present in the helper process's own environment here.
	for _, k := range []string{
		"UBAG_APP_SECRET", "UBAG_DATABASE_URL", "UBAG_POSTGRES_DSN", "UBAG_NATS_URL", "UBAG_MINIO_ACCESS_KEY",
		"UBAG_REMOTE_BROWSER_ENDPOINT", "UBAG_NOVNC_BASE_URL", "UBAG_FLEET_MANAGER_URL", "UBAG_ANTIGRAVITY_MODEL",
		"UBAG_WEBHOOK_SECRET", "UBAG_VOICE_RELAY_SECRET", "PGPASSWORD", "DATABASE_URL", "UBAG_MASTER_KEK_HEX",
	} {
		t.Setenv(k, "x-"+k)
	}
	t.Setenv("UBAG_BROWSER_ENGINE", "chromium") // allowlisted, non-secret: passes through
	t.Setenv("UBAG_HELPER_PLANE", "0")          // a stray value must not turn helper mode off

	r, err := New(Config{Python: os.Args[0], Script: "run_worker_daemon.py", Slots: 2, ProfileRoot: "/var/ubag/profiles"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	for slot := range 2 {
		env := r.pool.SlotCommand(slot).Env
		if err := helper.CheckEnvIsolation(env); err != nil {
			t.Fatalf("slot %d worker env: %v", slot, err)
		}
		for _, k := range []string{"UBAG_REMOTE_BROWSER_ENDPOINT", "UBAG_NOVNC_BASE_URL"} {
			if _, ok := lookup(env, k); ok {
				t.Fatalf("slot %d env carries %s", slot, k)
			}
		}
		if v, _ := lookup(env, "UBAG_HELPER_PLANE"); v != "1" {
			t.Fatalf("slot %d UBAG_HELPER_PLANE = %q, want 1", slot, v)
		}
		if v, _ := lookup(env, "UBAG_WORKER_SLOT_ID"); v != strconv.Itoa(slot) {
			t.Fatalf("slot %d UBAG_WORKER_SLOT_ID = %q", slot, v)
		}
		if v, _ := lookup(env, "UBAG_WORKER_POOL_SIZE"); v != "2" {
			t.Fatalf("slot %d UBAG_WORKER_POOL_SIZE = %q", slot, v)
		}
		if v, _ := lookup(env, "UBAG_PROFILE_DIR"); v != "/var/ubag/profiles" {
			t.Fatalf("slot %d UBAG_PROFILE_DIR = %q", slot, v)
		}
		if v, _ := lookup(env, "UBAG_BROWSER_ENGINE"); v != "chromium" {
			t.Fatal("allowlisted configuration must pass through")
		}
		for _, kv := range env {
			if strings.Contains(kv, "x-UBAG_") || strings.Contains(kv, "x-PG") || strings.Contains(kv, "x-DATABASE") {
				t.Fatalf("a primary-only value reached the worker: %q", strings.SplitN(kv, "=", 2)[0])
			}
		}
	}
}

func lookup(env []string, key string) (string, bool) {
	value, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			value, found = v, true // the last one wins, as in os/exec
		}
	}
	return value, found
}

// The runner's drop lists must cover what the primary's HelperAttemptSpec drops
// (the helper cannot import the executor, so the lists are duplicated).
func TestScrubCoversWhatThePrimarysHelperSpecDrops(t *testing.T) {
	opts := map[string]any{"user_data_dir": "/x", "profile_dir": "/y", "profile_path": "/z", "account_binding_id": "acct", "keep": 1}
	in := map[string]any{"attachment_local_paths": []any{"/p"}, "audio_local_path": "/a", "prompt": "hi"}
	env := executor.DispatchEnvelope{
		JobID: "job1", TenantID: "t1", Job: executor.DispatchJob{Target: "chatgpt_web", CommandType: "submit", Input: in, Options: opts},
	}
	ps, err := executor.NewHelperAttemptSpec(env, "att_1", "ident-1")
	if err != nil {
		t.Fatal(err)
	}
	inJSON, _ := json.Marshal(in)
	optJSON, _ := json.Marshal(opts)
	gotIn, gotOpt, err := scrubbed(helper.AttemptSpec{InputJSON: string(inJSON), OptionsJSON: string(optJSON)})
	if err != nil {
		t.Fatal(err)
	}
	for k := range opts {
		_, inPrimary := ps.Options[k]
		_, inHelper := gotOpt[k]
		if inPrimary != inHelper {
			t.Fatalf("option %q: primary spec keeps it=%v, helper keeps it=%v", k, inPrimary, inHelper)
		}
	}
	for k := range in {
		_, inPrimary := ps.Input[k]
		_, inHelper := gotIn[k]
		if inPrimary != inHelper {
			t.Fatalf("input %q: primary spec keeps it=%v, helper keeps it=%v", k, inPrimary, inHelper)
		}
	}
}

func TestWorkerPayloadCarriesOnlyTheIdentityRefAndScrubbedFields(t *testing.T) {
	g := newRig(t, 1, nil)
	s := spec("jobp", "ident-p", `{"mode":"payload","prompt":"hi","attachment_local_paths":["/etc/passwd"],"audio_local_path":"/etc/shadow"}`)
	s.OptionsJSON = `{"user_data_dir":"/other/tenant","profile_dir":"x","profile_path":"y","account_binding_id":"victim","headless":true}`
	sk, err := run(t, g.r, s)
	if err != nil {
		t.Fatal(err)
	}
	rep := payloadReport(t, sk)
	if rep["profile_ref"] != "ident-p" || rep["has_tenant"] != false || rep["has_callbacks"] != false || rep["target"] != "chatgpt_web" {
		t.Fatalf("report = %v", rep)
	}
	if rep["attempt"].(map[string]any)["id"] != "att_jobp" {
		t.Fatalf("attempt = %v", rep["attempt"])
	}
	opts := rep["options"].(map[string]any)
	if len(opts) != 1 || opts["headless"] != true {
		t.Fatalf("options = %v, want only headless", opts)
	}
	if _, ok := rep["files"]; ok {
		t.Fatalf("a caller-supplied attachment_local_paths reached the worker: %v", rep["files"])
	}
	if _, ok := rep["audio_local_path"]; ok {
		t.Fatal("a caller-supplied audio_local_path reached the worker")
	}
}

func payloadReport(t *testing.T, sk *sink) map[string]any {
	t.Helper()
	var res struct{ Text string }
	if err := json.Unmarshal([]byte(sk.outcome(t).GetResultJson()), &res); err != nil {
		t.Fatalf("outcome %v: %v", sk.outcome(t), err)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(res.Text), &rep); err != nil {
		t.Fatalf("report %q: %v", res.Text, err)
	}
	return rep
}

func TestIdentityRefTheWorkerWouldRefuseEndsFailedBeforeAnyWorker(t *testing.T) {
	g := newRig(t, 1, nil)
	// ':' passes the helper core's identity pattern but not the worker's profile_ref pattern.
	sk, err := run(t, g.r, spec("jobq", "ident:1", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if o := sk.outcome(t); o.GetErrorCode() != "helper_identity_ref_unsupported" || g.spawned.Load() != 0 {
		t.Fatalf("outcome %v, %d daemons started", o, g.spawned.Load())
	}
}

// ---- asset staging ---------------------------------------------------------

func hexSum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

const attachInput = `{"mode":"payload","prompt":"see file","attachments":[{"key":"doc1","kind":"document","content_type":"text/plain","filename":"notes.txt"}]}`

func assetFor(name string, b []byte) *helperv1.Asset {
	return &helperv1.Asset{Name: name, Sha256: hexSum(b), SizeBytes: int64(len(b)), MediaType: "text/plain"}
}

func TestAttachmentsAreStagedVerifiedAndCleanedUp(t *testing.T) {
	body := []byte("hello attachment")
	src := &fakeAssets{data: map[string][]byte{"doc1": body}}
	g := newRig(t, 1, func(c *Config) { c.Assets = src })
	s := spec("joba", "ident-a", attachInput)
	s.Assets = []*helperv1.Asset{assetFor("doc1", body)}
	sk, err := run(t, g.r, s)
	if err != nil {
		t.Fatal(err)
	}
	rep := payloadReport(t, sk)
	files, _ := rep["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("report = %v", rep)
	}
	f := files[0].(map[string]any)
	path := f["path"].(string)
	if f["body"] != string(body) || f["err"] != "<nil>" {
		t.Fatalf("the worker did not read the staged bytes: %v", f)
	}
	if !strings.Contains(path, "ubag-attach-") || !strings.HasSuffix(path, "notes.txt") {
		t.Fatalf("staged path %q: the worker's path guard needs the ubag-attach- temp prefix", path)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the staged file outlived the attempt: %v", err)
	}
}

func TestAttachmentIntegrityFailuresFailClosedBeforeAnyWorkerRuns(t *testing.T) {
	good := []byte("hello attachment")
	sum := assetFor("doc1", good)
	for name, tc := range map[string]struct {
		asset *helperv1.Asset
		open  func(string) (io.ReadCloser, error)
		code  string
		src   bool
	}{
		"wrong sha256": {asset: &helperv1.Asset{Name: "doc1", Sha256: hexSum([]byte("other")), SizeBytes: sum.SizeBytes}, code: "helper_asset_integrity", src: true},
		"bytes differ": {asset: sum, open: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("hello attachmenT")), nil }, code: "helper_asset_integrity", src: true},
		"short stream": {asset: sum, open: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("hello")), nil }, code: "helper_asset_integrity", src: true},
		"long stream": {asset: sum, open: func(string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(string(good) + "extra")), nil
		}, code: "helper_asset_integrity", src: true},
		"source failure": {asset: sum, open: func(string) (io.ReadCloser, error) { return nil, errors.New("down") }, code: "helper_assets_unavailable", src: true},
		"mid-read error": {asset: sum, open: func(string) (io.ReadCloser, error) {
			return io.NopCloser(io.MultiReader(strings.NewReader("hello"), errReader{})), nil
		}, code: "helper_assets_unavailable", src: true},
		"no source": {asset: sum, code: "helper_assets_unavailable"},
		"asset not declared": {asset: &helperv1.Asset{Name: "other", Sha256: sum.Sha256, SizeBytes: sum.SizeBytes}, open: func(string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(good)), nil
		}, code: "helper_assets_invalid", src: true},
	} {
		t.Run(name, func(t *testing.T) {
			var src *fakeAssets
			g := newRig(t, 1, func(c *Config) {
				if tc.src {
					src = &fakeAssets{data: map[string][]byte{"doc1": good}, open: tc.open}
					c.Assets = src
				}
			})
			s := spec("jobz", "ident-z", attachInput)
			s.Assets = []*helperv1.Asset{tc.asset}
			sk, err := run(t, g.r, s)
			if err != nil {
				t.Fatal(err)
			}
			o := sk.outcome(t)
			if o.GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_FAILED || o.GetErrorCode() != tc.code || o.GetSubmitted() {
				t.Fatalf("outcome = %v, want failed %s", o, tc.code)
			}
			if g.spawned.Load() != 0 {
				t.Fatal("a worker was started for an attempt whose attachments did not verify")
			}
			if slices.Contains(sk.types(), "PROMPT_SUBMITTED") {
				t.Fatal("the attempt reached the provider")
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestAssetListMustMatchTheDeclaredManifestOneToOne(t *testing.T) {
	good := []byte("x")
	src := &fakeAssets{data: map[string][]byte{"doc1": good, "doc2": good}}
	g := newRig(t, 1, func(c *Config) { c.Assets = src })
	for name, tc := range map[string]struct {
		input  string
		assets []*helperv1.Asset
	}{
		"declared but no asset":      {attachInput, nil},
		"asset but nothing declared": {`{"prompt":"x"}`, []*helperv1.Asset{assetFor("doc1", good)}},
		"extra asset":                {attachInput, []*helperv1.Asset{assetFor("doc1", good), assetFor("doc2", good)}},
		"duplicate asset names":      {attachInput, []*helperv1.Asset{assetFor("doc1", good), assetFor("doc1", good)}},
		"invalid manifest":           {`{"attachments":[{"key":"../x"}]}`, []*helperv1.Asset{assetFor("doc1", good)}},
	} {
		t.Run(name, func(t *testing.T) {
			s := spec("jobm", "ident-m", tc.input)
			s.Assets = tc.assets
			sk, err := run(t, g.r, s)
			if err != nil {
				t.Fatal(err)
			}
			if o := sk.outcome(t); o.GetErrorCode() != "helper_assets_invalid" {
				t.Fatalf("outcome = %v", o)
			}
		})
	}
	if len(src.opened) != 0 {
		t.Fatalf("assets were fetched for a mismatched manifest: %v", src.opened)
	}
}

func TestAudioArtifactKeyPointsAtItsStagedFile(t *testing.T) {
	body := []byte("RIFF")
	src := &fakeAssets{data: map[string][]byte{"voice1": body}}
	g := newRig(t, 1, func(c *Config) { c.Assets = src })
	s := spec("jobv", "ident-v", `{"mode":"payload","audio_artifact_key":"voice1","attachments":[{"key":"voice1","kind":"audio","content_type":"audio/wav"}]}`)
	s.Assets = []*helperv1.Asset{{Name: "voice1", Sha256: hexSum(body), SizeBytes: int64(len(body)), MediaType: "audio/wav"}}
	sk, err := run(t, g.r, s)
	if err != nil {
		t.Fatal(err)
	}
	rep := payloadReport(t, sk)
	path, _ := rep["audio_local_path"].(string)
	if !strings.HasSuffix(path, "voice1.wav") {
		t.Fatalf("audio_local_path = %q", path)
	}
}

// ---- through the real service ----------------------------------------------

type fakeStream struct {
	helperv1.HelperService_RunAttemptServer
	ctx context.Context
	mu  sync.Mutex
	got []*helperv1.AttemptEvent
}

func (f *fakeStream) Context() context.Context { return f.ctx }
func (f *fakeStream) Send(r *helperv1.RunAttemptResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, r.GetEvent())
	return nil
}

func newServer(t *testing.T, r *Runner, max int) *helper.Server {
	t.Helper()
	srv, err := helper.NewServer(helper.Config{
		NodeID: "helper-1", WorkloadVersion: "w1", RegistryDigest: strings.Repeat("a", 64), Runner: r, MaxAttempts: max,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv
}

func runReq(job, ident, input string) *helperv1.RunAttemptRequest {
	return &helperv1.RunAttemptRequest{
		Fence: &helperv1.Fence{
			JobId: job, AttemptId: "att_" + job, NodeId: "helper-1", LeaseGeneration: 1,
			LeaseExpiresAt:   timestamppb.New(time.Now().Add(2 * time.Minute)),
			InputFingerprint: "fp", WorkloadVersion: "w1",
		},
		Provider: "chatgpt_web", Target: "chatgpt_web", CommandType: "submit", IdentityRef: ident,
		InputJson: input, OptionsJson: `{}`, TraceId: "t", DeadlineSeconds: 30,
	}
}

// The runner behind the real service: the helper core's gate refuses a second
// attempt on a busy provider identity and a third beyond the node's capacity, and
// the first attempt's events and outcome come out of the stream.
func TestThroughTheServiceIdentityLockCapacityAndTheStream(t *testing.T) {
	g := newRig(t, 2, nil)
	srv := newServer(t, g.r, 2)

	first := &fakeStream{ctx: context.Background()}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- srv.RunAttempt(runReq("jobs1", "ident-1", `{"mode":"work","sleep_ms":600}`), first)
	}()
	waitFor(t, func() bool { return g.spawned.Load() > 0 })

	// Same provider identity: refused, while a free slot exists.
	err := srv.RunAttempt(runReq("jobs2", "ident-1", `{}`), &fakeStream{ctx: context.Background()})
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("second attempt on a busy identity = %v, want Unavailable (identity)", err)
	}
	// Another identity runs in parallel.
	other := &fakeStream{ctx: context.Background()}
	if err := srv.RunAttempt(runReq("jobs3", "ident-2", `{}`), other); err != nil {
		t.Fatalf("other identity: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	for _, st := range []*fakeStream{first, other} {
		last := st.got[len(st.got)-1]
		if last.GetType() != helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL || last.GetOutcome().GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED {
			t.Fatalf("stream ended with %v", last)
		}
		for i, e := range st.got {
			if e.GetSequence() != uint64(i+1) {
				t.Fatalf("event %d has sequence %d", i, e.GetSequence())
			}
		}
	}
}
