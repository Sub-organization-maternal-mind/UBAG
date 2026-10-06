package executor

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/jobcore"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// Voice lease excludes text and file jobs on the same browser (P5.5).
//
// A live voice session owns its browser environment: the virtual microphone and
// speaker monitor are instance-wide, and provider voice is a tab in that browser.
// A job that drives the same browser would switch tabs, change provider state or
// touch the audio under a live call. The two are therefore exclusive on the
// browser lane (topology.BrowserLaneKey), and this file is the job side of that:
//
//   - a job that drives the shared browser registers on its lane (LaneJob) and
//     THEN asks whether a voice admission is in flight or a live session holds
//     the lane; a held lane sends the job back to the queue after a delay (it is
//     never failed, never run, and not assigned);
//   - the voice side registers LaneVoice around its Reserve/Claim and looks for
//     running jobs first (httpapi/voice_handlers.go); register-then-look on both
//     sides is what makes the exclusion race-free (see topology/lane.go).
//
// Jobs that do not drive the shared browser are untouched: the mock target, SDK
// and CLI targets, a job with no remote browser endpoint (it has its own browser
// per run), and the gateway's own voice control jobs (activate/deactivate run FOR
// the session that holds the lane).

// VoiceLaneProbe answers whether a live voice session holds a browser lane right
// now. voice.LaneProbe implements it on the voice store, so the answer covers
// every replica, every restart and a terminating hold. An error must make the
// caller hold its work back, never run it.
type VoiceLaneProbe interface {
	VoiceHoldsLane(ctx context.Context, lane string) (bool, error)
}

// voiceLaneRetryDelay is how long a held-back job waits before its lease goes back
// to the queue (VoiceLaneRetryDelay; the pool overload delay by default, ADR-0011):
// the file spool re-queues a Retry instantly, so without the hold the job would
// spin.
func (c *WorkerConsumer) voiceLaneRetryDelay() time.Duration {
	if c.VoiceLaneRetryDelay > 0 {
		return c.VoiceLaneRetryDelay
	}
	return defaultOverloadRetryDelay
}

// laneCheckTimeout bounds one lane check (registry and voice store reads).
const laneCheckTimeout = 5 * time.Second

// laneStateUnavailable is the LaneBusyError reason for "could not tell"; the other
// reasons mean a voice session really is in the way.
const laneStateUnavailable = "lane_state_unavailable"

// LaneBusyError says a job could not start because its browser is not free for
// it. The job never reached a worker; the consumer retries its lease after
// RetryAfter (retryAfterDelay).
type LaneBusyError struct {
	// Reason is "voice_session_active", "voice_admission_in_flight" or
	// laneStateUnavailable (the registry or the probe could not answer, so the
	// job fails closed).
	Reason     string
	RetryAfter time.Duration
}

func (e *LaneBusyError) Error() string { return "browser lane busy: " + e.Reason }

// targetDrivesBrowser reports whether a target's jobs use the shared browser.
// Only the targets known not to are listed; anything else is assumed to, which
// is the fail-closed side.
func targetDrivesBrowser(target string) bool {
	switch strings.TrimSpace(target) {
	case "", "mock", "antigravity_sdk", "antigravity_cli":
		return false
	}
	return true
}

// browserLaneForJob is the browser lane a job drives, or "" when it shares none.
// Jobs reach the browser through the worker's UBAG_REMOTE_BROWSER_ENDPOINT, the
// same value daemonIdentityKey keys the pool on; without it each run launches its
// own browser and there is nothing to share.
func browserLaneForJob(envelope DispatchEnvelope) string {
	if jobcore.IsReservedCommandType(envelope.Job.CommandType) || !targetDrivesBrowser(envelope.Job.Target) {
		return ""
	}
	return topology.BrowserLaneKey(os.Getenv("UBAG_REMOTE_BROWSER_ENDPOINT"))
}

// enterBrowserLane registers the job on its browser lane and checks that no voice
// session holds it. It returns a nil hold (nothing to release) when voice
// exclusion is off or the job shares no browser, and a *LaneBusyError when the job
// must wait. Register FIRST, look second: the voice side does the reverse pair.
func (c *WorkerConsumer) enterBrowserLane(ctx context.Context, envelope DispatchEnvelope) (*topology.LaneHold, error) {
	if c.VoiceLanes == nil {
		return nil, nil
	}
	lane := browserLaneForJob(envelope)
	if lane == "" {
		return nil, nil
	}
	// A stalled store must hold the job back, not the consumer worker forever.
	ctx, cancel := context.WithTimeout(ctx, laneCheckTimeout)
	defer cancel()
	busy := func(reason string, cause error) (*topology.LaneHold, error) {
		if cause != nil {
			slog.Warn("browser lane check failed; holding the job back",
				"job_id", envelope.JobID, "error", cause)
		}
		return nil, &LaneBusyError{Reason: reason, RetryAfter: c.voiceLaneRetryDelay()}
	}
	hold, err := c.Concurrency.EnterLane(ctx, topology.LaneJob, lane)
	if err != nil {
		return busy(laneStateUnavailable, err)
	}
	admitting, err := c.Concurrency.LaneHolders(ctx, topology.LaneVoice, lane)
	if err != nil {
		hold.Release()
		return busy(laneStateUnavailable, err)
	}
	if admitting > 0 {
		hold.Release()
		return busy("voice_admission_in_flight", nil)
	}
	held, err := c.VoiceLanes.VoiceHoldsLane(ctx, lane)
	if err != nil {
		hold.Release()
		return busy(laneStateUnavailable, err)
	}
	if held {
		hold.Release()
		return busy("voice_session_active", nil)
	}
	return hold, nil
}
