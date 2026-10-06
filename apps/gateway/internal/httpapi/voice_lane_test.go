package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	voice "github.com/ubag/ubag/apps/gateway/internal/voice"
)

// P5.5, the voice-handler side of "voice excludes text and file jobs on the same
// browser": a voice admission skips a browser with a running job (the session
// queues), and, through the real consumer, a live session holds jobs back. The
// job-consumer side alone is in executor/voicelane_test.go.

const (
	laneEnvEndpoint  = "http://browser:9222"                  // what the gateway/worker env says
	laneInstanceSpec = "ws://Browser:9222/devtools/browser/g" // what the worker reports for browser-1
)

// voiceLaneServer is voiceTestServer with browser-1 registered at a CDP endpoint,
// a shared ConcurrencyRegistry and lane exclusion switched as asked.
func voiceLaneServer(t *testing.T, exclusion bool) (*Server, http.Handler, *topology.ConcurrencyRegistry) {
	t.Helper()
	registry := topology.NewConcurrencyRegistry()
	srv, handler, _ := voiceTestServer(t, func(c *Config) {
		c.Concurrency = registry
		c.VoiceLaneExclusion = exclusion
		c.Topology.(*topology.MemoryStore).AddInstance(topology.BrowserInstance{
			InstanceID: "browser-1", TenantID: "tenant_edge", State: "ready", RemoteEndpoint: laneInstanceSpec,
		})
	})
	return srv, handler, registry
}

func laneKey() string { return topology.BrowserLaneKey(laneEnvEndpoint) }

func laneHolders(t *testing.T, r *topology.ConcurrencyRegistry, kind topology.LaneKind) int {
	t.Helper()
	n, err := r.LaneHolders(t.Context(), kind, laneKey())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestVoiceQueuesWhileAJobRunsOnItsBrowserAndClaimsWhenItEnds(t *testing.T) {
	_, h, registry := voiceLaneServer(t, true)
	job, err := registry.EnterLane(t.Context(), topology.LaneJob, laneKey())
	if err != nil {
		t.Fatal(err)
	}

	code, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	if code != http.StatusAccepted || sess.Status != voice.StatusQueued || sess.InstanceRef != "" {
		t.Fatalf("create under a running job = %d %+v, want 202 queued with no lease", code, sess)
	}
	// Connecting while the job still runs is the documented "no environment free".
	rec := doJSON(h, http.MethodPost, "/v1/voice/sessions/"+sess.ID+"/connect", `{"sdp_offer":"v=0"}`, authHeaders(""))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "remains queued") {
		t.Fatalf("connect under a running job = %d %s, want 409 (still queued)", rec.Code, rec.Body.String())
	}
	if n := laneHolders(t, registry, topology.LaneVoice); n != 0 {
		t.Fatalf("a refused admission left %d voice registrations", n)
	}

	job.Release()
	rec = doJSON(h, http.MethodPost, "/v1/voice/sessions/"+sess.ID+"/connect", `{"sdp_offer":"v=0"}`, authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("connect once the job ended = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if n := laneHolders(t, registry, topology.LaneVoice); n != 0 {
		t.Fatalf("a finished admission left %d voice registrations (the lease is the hold, not the registry)", n)
	}
}

func TestVoiceAdmissionOnlyRespectsJobsOnItsOwnBrowser(t *testing.T) {
	srv, h, registry := voiceLaneServer(t, true)
	other, _ := registry.EnterLane(t.Context(), topology.LaneJob, topology.BrowserLaneKey("http://elsewhere:9222"))
	defer other.Release()
	if code, sess := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusCreated || sess.Status != voice.StatusConnecting {
		t.Fatalf("a job on another browser blocked the session: %d %+v", code, sess)
	}
	// A capability read looks at placements but never registers on a lane.
	srv.freeVoicePlacements(t.Context(), "tenant_edge", "chatgpt_web")
	for _, kind := range []topology.LaneKind{topology.LaneVoice, topology.LaneJob} {
		if n := laneHolders(t, registry, kind); n != 0 {
			t.Fatalf("%d %s registrations left on the lane", n, kind)
		}
	}
}

func TestVoiceAdmissionIgnoresWhatItCannotNameAndRespectsTheSwitch(t *testing.T) {
	// browser-1 has no registered endpoint (the default fixture): no shared lane.
	registry := topology.NewConcurrencyRegistry()
	_, noEndpoint, _ := voiceTestServer(t, func(c *Config) {
		c.Concurrency = registry
		c.VoiceLaneExclusion = true
	})
	job, _ := registry.EnterLane(t.Context(), topology.LaneJob, laneKey())
	defer job.Release()
	if code, sess := createVoice(t, noEndpoint, voiceBody("chatgpt_web")); code != http.StatusCreated || sess.Status != voice.StatusConnecting {
		t.Fatalf("an instance with no endpoint: %d %+v, want it admitted", code, sess)
	}

	// Exclusion off: a running job does not stop an admission (today's behaviour).
	_, off, offRegistry := voiceLaneServer(t, false)
	offJob, _ := offRegistry.EnterLane(t.Context(), topology.LaneJob, laneKey())
	defer offJob.Release()
	if code, sess := createVoice(t, off, voiceBody("chatgpt_web")); code != http.StatusCreated || sess.Status != voice.StatusConnecting {
		t.Fatalf("exclusion off: %d %+v, want it admitted", code, sess)
	}

	// Exclusion on but no registry to read: nothing to check, nothing is held back.
	_, noRegistry, _ := voiceTestServer(t, func(c *Config) {
		c.VoiceLaneExclusion = true
		c.Topology.(*topology.MemoryStore).AddInstance(topology.BrowserInstance{
			InstanceID: "browser-1", TenantID: "tenant_edge", State: "ready", RemoteEndpoint: laneInstanceSpec,
		})
	})
	if code, _ := createVoice(t, noRegistry, voiceBody("chatgpt_web")); code != http.StatusCreated {
		t.Fatalf("no registry: %d, want it admitted", code)
	}
}

// --- both directions, through the real handler and the real consumer ---------

type laneTestLease struct {
	jobID                             string
	envelope                          executor.DispatchEnvelope
	completed, retried, failed, other atomic.Bool
}

func (l *laneTestLease) JobID() string                        { return l.jobID }
func (l *laneTestLease) LeaseID() string                      { return "lease_" + l.jobID }
func (l *laneTestLease) QueueName() string                    { return "test" }
func (l *laneTestLease) Envelope() executor.DispatchEnvelope  { return l.envelope }
func (l *laneTestLease) Complete(context.Context) error       { l.completed.Store(true); return nil }
func (l *laneTestLease) Fail(context.Context) error           { l.failed.Store(true); return nil }
func (l *laneTestLease) Cancel(context.Context) error         { l.other.Store(true); return nil }
func (l *laneTestLease) Retry(context.Context) error          { l.retried.Store(true); return nil }
func (l *laneTestLease) Poison(context.Context, string) error { l.other.Store(true); return nil }

type laneTestQueue struct{ lease executor.WorkerLease }

func (laneTestQueue) Ready(context.Context) error { return nil }
func (q laneTestQueue) LeaseNext(context.Context) (executor.WorkerLease, bool, error) {
	return q.lease, true, nil
}

func TestVoiceAndBrowserJobsExcludeEachOtherOnOneBrowser(t *testing.T) {
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", laneEnvEndpoint)
	srv, h, registry := voiceLaneServer(t, true)
	jobs := jobstore.NewMemoryStore()
	var ran atomic.Int32
	entered, release := make(chan struct{}, 4), make(chan struct{})
	consumer := &executor.WorkerConsumer{
		Jobs:                jobs,
		Concurrency:         registry,
		VoiceLanes:          &voice.LaneProbe{Store: srv.voice, Topology: srv.topology},
		VoiceLaneRetryDelay: 20 * time.Millisecond,
		Runner: executor.WorkerRunFunc(func(_ context.Context, env executor.DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			ran.Add(1)
			entered <- struct{}{}
			<-release
			return []jobstore.WorkerEvent{
				{EventID: "e1_" + env.JobID, JobID: env.JobID, APIVersion: env.APIVersion, Type: "running", Sequence: 1, TraceID: env.TraceID, Data: map[string]any{"status": "running"}},
				{EventID: "e2_" + env.JobID, JobID: env.JobID, APIVersion: env.APIVersion, Type: "completed", Sequence: 2, TraceID: env.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "ok"}}},
			}, nil
		}),
	}
	newLease := func(key string) *laneTestLease {
		t.Helper()
		job, err := jobs.Create(t.Context(), jobstore.CreateRequest{
			APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: key,
			Target: "chatgpt_web", CommandType: "submit", Input: map[string]any{"prompt": "hi"}, TraceID: "trace_" + key,
		})
		if err != nil {
			t.Fatal(err)
		}
		return &laneTestLease{jobID: job.ID, envelope: executor.EnvelopeFromJob(job)}
	}
	runOnce := func(lease *laneTestLease) {
		consumer.Queue = laneTestQueue{lease: lease}
		if processed, err := consumer.RunOnce(context.Background()); err != nil || !processed {
			t.Errorf("RunOnce processed=%v err=%v", processed, err)
		}
	}

	// 1. A running job keeps voice off its browser: the session queues.
	running := newLease("lane_both_run_00001")
	done := make(chan struct{})
	go func() { defer close(done); runOnce(running) }()
	<-entered
	code, sess := createVoice(t, h, voiceBody("chatgpt_web"))
	if code != http.StatusAccepted || sess.Status != voice.StatusQueued {
		t.Fatalf("voice under a running job = %d %+v, want 202 queued", code, sess)
	}
	close(release) // from here on the runner no longer blocks
	<-done
	if !running.completed.Load() {
		t.Fatalf("the running job must finish normally: completed=%v retried=%v", running.completed.Load(), running.retried.Load())
	}

	// 2. The job is done, so the queued session takes the browser.
	rec := doJSON(h, http.MethodPost, "/v1/voice/sessions/"+sess.ID+"/connect", `{"sdp_offer":"v=0"}`, authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("connect after the job = %d %s", rec.Code, rec.Body.String())
	}

	// 3. A live session keeps jobs off its browser: the next job goes back to the queue.
	ranBefore := ran.Load()
	held := newLease("lane_both_held_00002")
	runOnce(held)
	if !held.retried.Load() || held.completed.Load() || held.failed.Load() || ran.Load() != ranBefore {
		t.Fatalf("a job under a live voice session must be held back, not run: ran=%d retried=%v", ran.Load()-ranBefore, held.retried.Load())
	}

	// 4. The session ends, and the same kind of job runs again.
	if rec := doJSON(h, http.MethodPost, "/v1/voice/sessions/"+sess.ID+"/terminate", `{}`, authHeaders("")); rec.Code != http.StatusOK {
		t.Fatalf("terminate = %d %s", rec.Code, rec.Body.String())
	}
	resumed := newLease("lane_both_after_00003")
	runOnce(resumed)
	if !resumed.completed.Load() || ran.Load() != ranBefore+1 {
		t.Fatalf("a job after the session ended must run: completed=%v ran=%d", resumed.completed.Load(), ran.Load()-ranBefore)
	}
}

// unavailableMedia is a media plane that can never connect (no relay secret).
type unavailableMedia struct{ fakeMediaNegotiator }

func (*unavailableMedia) MediaAvailable() bool { return false }

// With lane exclusion on, a live session on a media plane that can never connect
// must not be admitted: it would pin a browser lane for its whole lease (security
// review, P5.5). With it off (the default) create is unchanged (rules review).
func TestVoiceLiveSessionRefusedWhileMediaCannotConnect(t *testing.T) {
	for _, exclusion := range []bool{true, false} {
		registry := topology.NewConcurrencyRegistry()
		_, h, _ := voiceTestServer(t, func(c *Config) {
			c.VoiceMedia = &unavailableMedia{}
			c.Concurrency = registry
			c.VoiceLaneExclusion = exclusion
		})
		rec := doJSON(h, http.MethodPost, "/v1/voice/sessions", voiceBody("chatgpt_web"), authHeaders(""))
		refused := rec.Code == http.StatusServiceUnavailable && strings.Contains(rec.Body.String(), "UBAG-VOICE-MEDIA-UNAVAILABLE-007")
		if refused != exclusion {
			t.Fatalf("exclusion=%v: create = %d %s", exclusion, rec.Code, rec.Body.String())
		}
	}
}
