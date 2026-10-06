package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	voice "github.com/ubag/ubag/apps/gateway/internal/voice"
)

// Provider voice lifecycle.
//
// Starting provider voice is browser work, so it rides the existing job
// pipeline: the gateway creates an INTERNAL control job (command_type
// "voice.activate" / "voice.deactivate") that the worker runs against the
// leased environment's CDP endpoint. The control job:
//   - is owned by the session's tenant under a dedicated app id, so it never
//     appears in the tenant application's own job list;
//   - skips the public admission path (it is not caller work) and carries a
//     high priority and a hard timeout so it cannot sit behind chat turns;
//   - can never be created by external callers (jobcore rejects the reserved
//     "voice." command prefix on every external path).
//
// A session reaches StatusConnected only when BOTH the provider reported a
// verified voice-ready state AND the client's media connection is up — never
// from SDP creation or a click alone. Activation failure terminates the
// session with the worker's explicit state as the reason.
const (
	voiceControlAppID   = "ubag-internal-voice"
	voiceControlTimeout = 75 * time.Second
	voiceControlPoll    = 300 * time.Millisecond
	// voiceDeactivateTimeout bounds best-effort teardown work.
	voiceDeactivateTimeout = 45 * time.Second
)

// voiceLifecycle tracks, per session on THIS replica, whether provider voice
// has been requested and which of the two readiness conditions hold. The
// media connection and the activation request both originate on the replica
// that served /connect, so the two signals meet here.
type voiceLifecycle struct {
	mu       sync.Mutex
	sessions map[string]*voiceLifeState
}

type voiceLifeState struct {
	providerReady bool
	peerUp        bool
	failed        bool
}

func (l *voiceLifecycle) get(id string, create bool) *voiceLifeState {
	if l.sessions == nil {
		if !create {
			return nil
		}
		l.sessions = map[string]*voiceLifeState{}
	}
	st := l.sessions[id]
	if st == nil && create {
		st = &voiceLifeState{}
		l.sessions[id] = st
	}
	return st
}

// voiceInstanceEndpoint resolves the CDP endpoint of the session's leased
// browser environment from the tenant's topology — never from client input.
func (s *Server) voiceInstanceEndpoint(ctx context.Context, sess voice.Session) string {
	if s.topology == nil {
		return ""
	}
	instances, err := s.topology.ListInstances(ctx, topology.InstanceFilter{TenantID: sess.TenantID, Limit: 100})
	if err != nil {
		return ""
	}
	for _, instance := range instances {
		if instance.InstanceID == sess.InstanceRef {
			return strings.TrimSpace(instance.RemoteEndpoint)
		}
	}
	return ""
}

// voiceContextIndex returns the zero-based browser-context index the worker
// must attach to when the session's environment hosts several provider
// contexts (the worker fails closed on an ambiguous browser otherwise). The
// index is the session's own (target, identity) context among the instance's
// topology contexts in creation order, which mirrors the order the browser
// opened them. ok=false with a nil error means a single-context environment
// (or the flag is off): no index is sent and today's behaviour is unchanged.
// A multi-context environment whose context cannot be pinned fails closed
// instead of guessing.
func (s *Server) voiceContextIndex(ctx context.Context, sess voice.Session) (index int, ok bool, err error) {
	if !s.voiceContextIdx || s.topology == nil {
		return 0, false, nil
	}
	contexts, err := s.topology.ListContexts(ctx, topology.ContextFilter{TenantID: sess.TenantID, InstanceID: sess.InstanceRef, Limit: 1000})
	if err != nil {
		return 0, false, errors.New("listing the environment's browser contexts failed")
	}
	if len(contexts) <= 1 {
		return 0, false, nil
	}
	sort.SliceStable(contexts, func(i, j int) bool {
		if !contexts[i].CreatedAt.Equal(contexts[j].CreatedAt) {
			return contexts[i].CreatedAt.Before(contexts[j].CreatedAt)
		}
		return contexts[i].ContextID < contexts[j].ContextID
	})
	match := -1
	for i, c := range contexts {
		if c.TargetID == sess.Target && c.IdentityRef == sess.IdentityRef && c.InstanceID == sess.InstanceRef {
			if match >= 0 {
				return 0, false, errors.New("browser context is ambiguous for this session's identity")
			}
			match = i
		}
	}
	if match < 0 {
		return 0, false, errors.New("session's browser context is not registered on its environment")
	}
	return match, true, nil
}

// runVoiceControl creates and dispatches one control job and waits for its
// terminal result. It returns the worker's result map (always carrying
// "state" on success) or an error describing why no verified result exists.
func (s *Server) runVoiceControl(ctx context.Context, sess voice.Session, action string, timeout time.Duration) (map[string]any, error) {
	if s.jobs == nil || s.executor == nil {
		return nil, errors.New("job pipeline is not configured")
	}
	endpoint := s.voiceInstanceEndpoint(ctx, sess)
	if endpoint == "" {
		return nil, errors.New("leased browser environment has no registered CDP endpoint")
	}
	input := map[string]any{
		"provider_id":  sess.Target,
		"cdp_endpoint": endpoint,
		"action":       action,
	}
	index, hasIndex, err := s.voiceContextIndex(ctx, sess)
	if err != nil {
		return nil, err
	}
	if hasIndex {
		input["context"] = index
	}
	job, err := s.jobs.Create(ctx, jobstore.CreateRequest{
		APIVersion:     s.apiVersion,
		TenantID:       sess.TenantID,
		AppID:          voiceControlAppID,
		IdempotencyKey: fmt.Sprintf("voice:%s:%s:%d", sess.ID, action, time.Now().UnixNano()),
		Target:         sess.Target,
		CommandType:    "voice." + action,
		Client: map[string]any{
			"app_id":      voiceControlAppID,
			"app_version": "1",
			"sdk":         map[string]any{"name": "ubag-gateway", "version": "1"},
		},
		Input:   input,
		Options: map[string]any{"priority": "high", "timeout_seconds": int(timeout.Seconds())},
		TraceID: generatedTraceID(),
	})
	if err != nil {
		return nil, fmt.Errorf("create control job: %w", err)
	}
	receipt, err := s.executor.EnqueueJob(ctx, job)
	if err != nil {
		_, _, _ = s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusFailedRetryable)
		return nil, fmt.Errorf("dispatch control job: %w", err)
	}
	if receipt.Backend == "noop" {
		_, _, _ = s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusCanceled)
		return nil, errors.New("no worker backend is configured to run provider voice (executor is noop)")
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lastSeq := 0
	for {
		events, found, err := s.jobs.WaitEvents(waitCtx, job.ID, lastSeq, 1)
		if err != nil || !found {
			if waitCtx.Err() != nil {
				s.cancelFacadeJob(context.Background(), job, "voice_control_timeout")
				return nil, errors.New("timed out waiting for the worker")
			}
			return nil, fmt.Errorf("wait for control job: %v", err)
		}
		for _, ev := range events {
			if ev.Sequence > lastSeq {
				lastSeq = ev.Sequence
			}
		}
		current, ok, err := s.jobs.Get(ctx, job.ID)
		if err != nil || !ok {
			return nil, errors.New("control job disappeared")
		}
		if !jobstore.TerminalStatus(current.Status) {
			continue
		}
		if current.Status == jobstore.StatusCompleted {
			if result, ok := current.Result.(map[string]any); ok {
				if inner, ok := result["result"].(map[string]any); ok {
					return inner, nil
				}
				return result, nil
			}
			return nil, errors.New("control job completed without a result")
		}
		return nil, fmt.Errorf("control job ended %s: %s", current.Status, s.voiceControlFailureState(ctx, job.ID))
	}
}

// voiceControlFailureState digs the worker's explicit state out of the
// control job's last events (failed jobs carry no Result).
func (s *Server) voiceControlFailureState(ctx context.Context, jobID string) string {
	events, _, err := s.jobs.ListEvents(ctx, jobID, 0, 50)
	if err != nil {
		return "unknown"
	}
	for i := len(events) - 1; i >= 0; i-- {
		data := events[i].Data
		for _, key := range []string{"state", "reason", "error"} {
			if v, ok := data[key].(string); ok && v != "" {
				return v
			}
		}
		if inner, ok := data["result"].(map[string]any); ok {
			if v, ok := inner["state"].(string); ok && v != "" {
				return v
			}
		}
	}
	return "unknown"
}

// beginVoiceActivation requests provider voice for a session once. Reconnects
// (replacement media) do not re-activate: the provider's voice session is
// still live.
func (s *Server) beginVoiceActivation(sess voice.Session) {
	if !s.voiceActivation || sess.NodeID != "" {
		// A session hosted on a Helper Node is activated by that node, against its
		// own loopback browser, as part of the offer (D7); the primary never runs a
		// control job for an environment it has no CDP endpoint for.
		return
	}
	s.voiceLife.mu.Lock()
	if s.voiceLife.get(sess.ID, false) != nil {
		s.voiceLife.mu.Unlock()
		return
	}
	s.voiceLife.get(sess.ID, true)
	s.voiceLife.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), voiceControlTimeout+10*time.Second)
		defer cancel()
		result, err := s.runVoiceControl(ctx, sess, "activate", voiceControlTimeout)
		state := ""
		if err == nil {
			state, _ = result["state"].(string)
		}
		if err != nil || state != "activated" {
			reason := state
			if err != nil {
				reason = err.Error()
			}
			slog.Warn("provider voice activation failed", "session_id", sess.ID, "target", sess.Target, "reason", reason)
			s.failVoiceSession(sess, "activation_failed: "+truncateReason(reason))
			return
		}
		s.voiceLife.mu.Lock()
		if st := s.voiceLife.get(sess.ID, false); st != nil {
			st.providerReady = true
		}
		s.voiceLife.mu.Unlock()
		s.maybeMarkVoiceConnected(sess)
	}()
}

func truncateReason(reason string) string {
	const max = 200
	if len(reason) > max {
		return reason[:max]
	}
	return reason
}

// terminateVoiceSession ends a session record. With the terminating hold on
// (UBAG_HELPER_VOICE) and provider voice requested on this replica, the account
// and environment stay reserved until the deactivate job acks (VoiceMediaEnded)
// or voice.TerminatingHoldMax passes, so a successor cannot activate on a
// provider UI that is still being torn down. Otherwise leases free at once, as
// before. The caller drops the media path AFTER this returns, which is what
// fires VoiceMediaEnded, so the hold always exists before its ack.
func (s *Server) terminateVoiceSession(ctx context.Context, tenantID, sessionID string, now time.Time, reason string) error {
	if s.voiceHold {
		s.voiceLife.mu.Lock()
		requested := s.voiceLife.get(sessionID, false) != nil
		s.voiceLife.mu.Unlock()
		if requested {
			return s.voice.BeginTerminate(ctx, tenantID, sessionID, now, reason, voice.TerminatingHoldMax)
		}
	}
	return s.voice.Terminate(ctx, tenantID, sessionID, now, reason)
}

// failVoiceSession ends a session whose provider voice could not be started:
// terminate the record (freeing its leases) and drop the media path.
func (s *Server) failVoiceSession(sess voice.Session, reason string) {
	s.voiceLife.mu.Lock()
	if st := s.voiceLife.get(sess.ID, false); st != nil {
		st.failed = true
	}
	s.voiceLife.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.terminateVoiceSession(ctx, sess.TenantID, sess.ID, time.Now().UTC(), reason); err != nil {
		slog.Error("terminating voice session after activation failure failed", "session_id", sess.ID, "error", err)
	}
	s.dropVoiceMedia(sess.ID)
}

// maybeMarkVoiceConnected advances connecting -> connected once the provider
// is verified ready AND the client's media connection is up.
func (s *Server) maybeMarkVoiceConnected(sess voice.Session) {
	s.voiceLife.mu.Lock()
	st := s.voiceLife.get(sess.ID, false)
	ready := st != nil && st.providerReady && st.peerUp && !st.failed
	s.voiceLife.mu.Unlock()
	if !ready {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.voice.Transition(ctx, sess.TenantID, sess.ID, voice.StatusConnecting, voice.StatusConnected, time.Now().UTC(), "")
	if err != nil && !errors.Is(err, voice.ErrConflict) && !errors.Is(err, voice.ErrNotFound) {
		slog.Error("marking voice session connected failed", "session_id", sess.ID, "error", err)
	}
}

// VoiceMediaConnected is the MediaHub OnConnected hook: the client's peer
// connection is up. It only records the signal; the session is marked
// connected once the provider is also verified ready.
func (s *Server) VoiceMediaConnected(sess voice.Session) {
	if !s.voiceActivation {
		return
	}
	s.voiceLife.mu.Lock()
	if st := s.voiceLife.get(sess.ID, false); st != nil {
		st.peerUp = true
	}
	s.voiceLife.mu.Unlock()
	s.maybeMarkVoiceConnected(sess)
}

// VoiceMediaEnded is the MediaHub OnEnded hook, fired once for EVERY end of a
// media path on this replica (terminate, lease sweep, reconciliation, peer
// failure, shutdown). It stops provider voice best-effort so the provider's
// live session does not outlive the lease, then forgets the session. Reconnect
// replacement is not an end and does not reach here.
func (s *Server) VoiceMediaEnded(sess voice.Session, reason string) {
	if !s.voiceActivation {
		return
	}
	s.voiceLife.mu.Lock()
	st := s.voiceLife.get(sess.ID, false)
	delete(s.voiceLife.sessions, sess.ID)
	s.voiceLife.mu.Unlock()
	if st == nil {
		return // activation was never requested
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), voiceDeactivateTimeout+10*time.Second)
		defer cancel()
		if _, err := s.runVoiceControl(ctx, sess, "deactivate", voiceDeactivateTimeout); err != nil {
			// No ack: a terminating hold (if any) runs out by itself at its cap.
			slog.Warn("provider voice deactivation failed", "session_id", sess.ID, "reason", reason, "error", err)
			return
		}
		if s.voiceHold {
			// The provider confirmed deactivation: free the leases the terminate
			// kept. A session that was never held reads as an idempotent ack.
			if err := s.voice.ReleaseHold(ctx, sess.TenantID, sess.ID, voice.LeaseFence{}, time.Now().UTC()); err != nil &&
				!errors.Is(err, voice.ErrNotFound) && !errors.Is(err, voice.ErrConflict) {
				slog.Warn("releasing the voice terminating hold failed", "session_id", sess.ID, "error", err)
			}
		}
	}()
}
