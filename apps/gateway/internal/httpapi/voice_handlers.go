package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
	voice "github.com/ubag/ubag/apps/gateway/internal/voice"
	"github.com/ubag/ubag/apps/gateway/internal/voiceplace"
)

// Voice-session HTTP surface (multimodal/voice release):
//
//	POST   /v1/voice/sessions                      create (queues when no account+environment is free)
//	GET    /v1/voice/sessions                      list this tenant's sessions
//	GET    /v1/voice/sessions/{id}                 status
//	POST   /v1/voice/sessions/{id}/connect         claim leases (if queued) + negotiate the media connection
//	POST   /v1/voice/sessions/{id}/mute            {muted: bool} control channel
//	POST   /v1/voice/sessions/{id}/renew           extend the lease (client heartbeat)
//	POST   /v1/voice/sessions/{id}/terminate       terminate + release (also DELETE /{id})
//
// Auth rides the shared middleware; create/connect/mute/renew/terminate
// require the job:create action (a voice session consumes a provider account
// exactly like a submitted job does), reads require job:read. Everything is
// tenant-scoped from the authenticated principal — never from the body.
//
// The media plane is a separate component (VoiceMedia): connect returns the
// SDP answer plus a short-lived, session-scoped media credential. Cleanup on
// disconnect is the sweeper's lease expiry plus explicit terminate; a client
// that walks away without terminating cannot pin a provider account longer
// than the TTL.
const (
	// defaultVoiceSessionTTL bounds one lease between renewals.
	defaultVoiceSessionTTL = 10 * time.Minute
	minVoiceSessionTTL     = 30 * time.Second
	maxVoiceSessionTTL     = time.Hour
	// defaultVoiceMaxSessionsPerTenant caps ACTIVE (lease-holding) sessions;
	// excess sessions queue instead of failing.
	defaultVoiceMaxSessionsPerTenant = 4
	// defaultVoiceMaxQueuedPerTenant caps the queued backlog so a runaway
	// client cannot grow the store without bound.
	defaultVoiceMaxQueuedPerTenant = 32
	// voiceMediaCredentialTTL bounds the scoped media credential issued at
	// connect. Short by design: a leaked credential outlives nothing.
	voiceMediaCredentialTTL = 5 * time.Minute
)

// MediaNegotiator terminates the client side of the WebRTC media connection
// for a voice session: it validates the offer, produces the answer, and owns
// the relay between that connection and the provider browser's audio
// environment. The gateway never parses SDP itself — it only authenticates,
// authorizes, leases, and hands the offer to this component.
type MediaNegotiator interface {
	// HandleOffer negotiates one connection for an ACTIVE (lease-holding)
	// session. The returned answer is plain SDP text.
	HandleOffer(ctx context.Context, session voice.Session, sdpOffer string) (answer string, err error)
}

type voiceSessionCreateRequest struct {
	Target      string `json:"target"`
	IdentityRef string `json:"identity_ref,omitempty"`
	TTLSeconds  *int   `json:"ttl_seconds,omitempty"`
	// Mode selects the session kind: "live" (default) is a two-way voice
	// session holding exclusive leases; "utterance" is the separately
	// selectable audio-in/text-out mode backed by a transcription-style job
	// (the response carries job_id; upload the audio to
	// /v1/jobs/{job_id}/artifacts/{key}). A live session is NEVER silently
	// substituted with an utterance job — unknown modes are rejected.
	Mode string `json:"mode,omitempty"`
}

type voiceSessionMuteRequest struct {
	Muted *bool `json:"muted"`
}

type voiceSessionConnectRequest struct {
	SDPOffer string `json:"sdp_offer"`
}

type voiceSessionConnectResponse struct {
	SessionID       string `json:"session_id"`
	Status          string `json:"status"`
	SDPAnswer       string `json:"sdp_answer,omitempty"`
	MediaCredential string `json:"media_credential,omitempty"`
	MediaExpiresMS  int64  `json:"media_credential_expires_ms,omitempty"`
	// ICEServers are the STUN/TURN servers (TURN with time-limited
	// credentials) the client must configure its RTCPeerConnection with.
	ICEServers []voice.ICEServer `json:"ice_servers,omitempty"`
	Kind       string            `json:"kind"`
}

func (s *Server) voiceError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool, retryAfterMS *int) {
	s.writeError(w, r, status, apiError{
		Code:         code,
		Category:     "voice",
		Message:      message,
		Retryable:    retryable,
		RetryAfterMS: retryAfterMS,
	})
}

func (s *Server) mapVoiceStoreError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, voice.ErrNotFound):
		s.voiceError(w, r, http.StatusNotFound, "UBAG-VOICE-SESSION-NOT-FOUND-006",
			"voice session does not exist in this tenant", false, nil)
	case errors.Is(err, voice.ErrConflict):
		s.voiceError(w, r, http.StatusConflict, "UBAG-VOICE-SESSION-STATE-005",
			"voice session changed state concurrently; re-read its status", false, nil)
	default:
		s.writeError(w, r, http.StatusInternalServerError, internalError("voice session store error"))
	}
	return true
}

// resolveVoiceSessionTTL resolves and clamps the requested lease TTL against
// the operator-configured default.
func (s *Server) resolveVoiceSessionTTL(requested *int) (time.Duration, bool) {
	ttl := defaultVoiceSessionTTL
	if s.voiceSessionTTL > 0 {
		ttl = s.voiceSessionTTL
	}
	if requested != nil {
		ttl = time.Duration(*requested) * time.Second
	}
	if ttl < minVoiceSessionTTL || ttl > maxVoiceSessionTTL {
		return 0, false
	}
	return ttl, true
}

func newVoiceSessionID(now time.Time) string {
	var entropy [6]byte
	_, _ = rand.Read(entropy[:])
	return fmt.Sprintf("voice_%d_%s", now.Unix(), hex.EncodeToString(entropy[:]))
}

// voiceCandidates resolves, server-side, the (provider account, browser
// environment) pairs this tenant may hold for the target: an authenticated
// topology context paired with the usable instance that actually hosts it.
// A caller-supplied identity_ref is only a PREFERENCE among these pairs — it
// is never proof of ownership, so naming an account the tenant does not own
// (or one without a hosting environment) resolves to nothing. The second result
// maps each usable instance with a registered CDP endpoint to its browser lane.
func (s *Server) voiceCandidates(ctx context.Context, tenantID, target, preferred string) ([]voice.Placement, map[string]string) {
	if s.topology == nil {
		return nil, nil
	}
	instances, err := s.topology.ListInstances(ctx, topology.InstanceFilter{TenantID: tenantID, Limit: 100})
	if err != nil {
		return nil, nil
	}
	usable := map[string]bool{}
	lanes := map[string]string{} // instance -> browser lane (only instances with a CDP endpoint)
	for _, instance := range instances {
		switch strings.ToLower(strings.TrimSpace(instance.State)) {
		case "failed", "unhealthy", "draining", "recycling", "stopped", "terminated":
		default:
			if id := strings.TrimSpace(instance.InstanceID); id != "" {
				usable[id] = true
				if lane := topology.BrowserLaneKey(instance.RemoteEndpoint); lane != "" {
					lanes[id] = lane
				}
			}
		}
	}
	contexts, err := s.topology.ListContexts(ctx, topology.ContextFilter{TenantID: tenantID, Limit: 1000})
	if err != nil {
		return nil, nil
	}
	preferred = strings.TrimSpace(preferred)
	var out []voice.Placement
	for _, c := range contexts {
		identity, instance := strings.TrimSpace(c.IdentityRef), strings.TrimSpace(c.InstanceID)
		if c.TargetID != target || c.LoginState != "authenticated" || identity == "" || !usable[instance] {
			continue
		}
		p := voice.Placement{Identity: identity, Instance: instance}
		if identity == preferred {
			out = append([]voice.Placement{p}, out...)
		} else {
			out = append(out, p)
		}
	}
	return out, lanes
}

// voicePlacements is voiceCandidates for an admission (Reserve or Claim): pairs
// whose browser has a running job are left out (voice and jobs exclude each other
// on a browser, see admitVoiceLanes), and with helper-hosted voice on (VoiceNodes)
// an account whose profile lives on a Helper Node is kept only when that node can
// take the call (see voiceplace). The returned settle must be called exactly once,
// right after the store call that uses the placements returns, with the session it
// returned (the zero Session when it failed): it releases the browser-lane
// registrations and every node reservation the session did not win, keeps the
// winner's node slot for the session's life, and returns the Helper Node that
// hosts the session ("" for a primary-hosted or queued one).
func (s *Server) voicePlacements(ctx context.Context, tenantID, target, preferred string) ([]voice.Placement, func(voice.Session) string) {
	candidates, lanes := s.voiceCandidates(ctx, tenantID, target, preferred)
	kept, releaseLanes := s.admitVoiceLanes(ctx, candidates, lanes)
	if s.voiceNodes == nil {
		return kept, func(voice.Session) string { releaseLanes(); return "" }
	}
	admission := s.voiceNodes.Admit(ctx, tenantID, target, voiceNodeCandidates(kept, lanes))
	return admission.Placements(), func(won voice.Session) string {
		releaseLanes()
		return admission.Settle(won)
	}
}

// voiceNodeCandidates tells the node placer which candidates' environments carry a
// CDP endpoint in the topology (the instances in lanes).
func voiceNodeCandidates(placements []voice.Placement, lanes map[string]string) []voiceplace.Candidate {
	out := make([]voiceplace.Candidate, len(placements))
	for i, p := range placements {
		out[i] = voiceplace.Candidate{Placement: p, CDPEndpoint: lanes[p.Instance] != ""}
	}
	return out
}

// bindVoiceNode hands the media lease of a freshly admitted session to the Helper
// Node that holds its slot, so every later write and every control call is fenced
// by (node, generation) (P5.8). A session that cannot be bound is ended: it would
// hold an account and an environment for a node that cannot serve it.
func (s *Server) bindVoiceNode(ctx context.Context, session voice.Session, nodeID string, mediaTTL time.Duration, now time.Time) (voice.Session, error) {
	bound, err := s.voice.BindNode(ctx, session.TenantID, session.ID, nodeID, mediaTTL, now)
	if err != nil {
		slog.Error("binding a voice session to its helper node failed; ending it", "session_id", session.ID, "error", err)
		_ = s.voice.Terminate(ctx, session.TenantID, session.ID, now, "node_bind_failed")
		s.releaseVoiceNodeHolds(ctx)
		return voice.Session{}, err
	}
	return bound, nil
}

// releaseVoiceNodeHolds frees the node slots of sessions that no longer reserve
// their environment (a no-op without VoiceNodes). Every path that ends a session
// calls it; the periodic release in serve covers the ones that cannot (a lease
// sweep, a peer failure, a terminate served by another replica).
func (s *Server) releaseVoiceNodeHolds(ctx context.Context) {
	if s.voiceNodes == nil || s.voice == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.voiceNodes.ReleaseEnded(ctx, s.voice, time.Now().UTC())
}

// NodeMediaNegotiator is a MediaNegotiator that terminates the media of a session
// bound to a Helper Node (session.NodeID) on that node (P5.11). The primary's own
// media hub is not one: a node-bound session must never reach it, because the
// hub resolves a relay for the session's environment on the primary and could
// attach the call to a different account's browser.
type NodeMediaNegotiator interface {
	MediaNegotiator
	HostsNodeSessions() bool
}

func hostsNodeSessions(m MediaNegotiator) bool {
	n, ok := m.(NodeMediaNegotiator)
	return ok && n.HostsNodeSessions()
}

// admitVoiceLanes keeps the placements whose browser has no running job and holds
// a voice-admission registration on each browser it keeps, so a job that starts
// meanwhile is held back (the consumer registers, then looks for it). The caller
// releases the registrations once Reserve or Claim has returned: from then on the
// session's own lease, which the consumer reads from the voice store, holds the
// browser. Register-then-look on both sides is what makes the exclusion race-free
// (topology/lane.go). A browser whose state cannot be read is left out (fail
// closed); an instance with no registered endpoint has no shared lane to check.
func (s *Server) admitVoiceLanes(ctx context.Context, placements []voice.Placement, lanes map[string]string) ([]voice.Placement, func()) {
	if !s.voiceLanes {
		return placements, func() {}
	}
	var holds []*topology.LaneHold
	kept := make([]voice.Placement, 0, len(placements))
	for _, p := range placements {
		lane := lanes[p.Instance]
		if lane == "" {
			kept = append(kept, p)
			continue
		}
		hold, err := s.concurrency.EnterLane(ctx, topology.LaneVoice, lane)
		if err != nil {
			slog.Warn("voice admission: browser lane unavailable; skipping its placement", "error", err)
			continue
		}
		if jobs, err := s.concurrency.LaneHolders(ctx, topology.LaneJob, lane); err != nil || jobs > 0 {
			if err != nil {
				slog.Warn("voice admission: browser lane unreadable; skipping its placement", "error", err)
			}
			hold.Release()
			continue
		}
		holds = append(holds, hold)
		kept = append(kept, p)
	}
	return kept, func() {
		for _, hold := range holds {
			hold.Release()
		}
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func (s *Server) handleVoiceSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleVoiceSessionCreate(w, r)
	case http.MethodGet:
		s.handleVoiceSessionList(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodPost, http.MethodGet)
	}
}

func (s *Server) handleVoiceSessionCreate(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		s.voiceError(w, r, http.StatusNotImplemented, "UBAG-VOICE-NOT-CONFIGURED-002",
			"voice sessions are not configured on this gateway", false, nil)
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}
	var req voiceSessionCreateRequest
	if !s.decodeVoiceJSON(w, r, &req, 16<<10) {
		return
	}
	target := strings.TrimSpace(req.Target)
	if target == "" || !isTargetKey(target) || !facadeTargetKnown(target) {
		s.voiceError(w, r, http.StatusBadRequest, "UBAG-VOICE-UNSUPPORTED-001",
			fmt.Sprintf("target %q is not a known live target", req.Target), false, nil)
		return
	}
	if !resolveVoiceCapability(target).Live {
		s.voiceError(w, r, http.StatusBadRequest, "UBAG-VOICE-UNSUPPORTED-001",
			fmt.Sprintf("live voice is not supported for target %q; consult GET /v1/capabilities", target), false, nil)
		return
	}
	ttl, okTTL := s.resolveVoiceSessionTTL(req.TTLSeconds)
	if !okTTL {
		s.voiceError(w, r, http.StatusBadRequest, "UBAG-VOICE-SESSION-STATE-005",
			"ttl_seconds must be between 30 and 3600", false, nil)
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = voice.ModeLive
	}
	if mode != voice.ModeLive && mode != voice.ModeUtterance {
		s.voiceError(w, r, http.StatusBadRequest, "UBAG-VOICE-SESSION-STATE-005",
			"mode must be \"live\" or \"utterance\"; live voice is never substituted with utterance jobs", false, nil)
		return
	}
	tenantID, appID := requestScope(r)
	ctx := r.Context()
	now := time.Now().UTC()

	// Utterance mode: the session's resource is a transcription-style JOB,
	// not an exclusive live-voice appointment. No account or environment
	// lease is claimed (they stay free for live sessions); the caller
	// uploads the audio to the job and polls it like any transcription.
	if mode == voice.ModeUtterance {
		utteranceTarget := target
		policy := resolveAttachmentPolicy(target)
		if !attachmentKindsAllowAudio(policy) {
			utteranceTarget = defaultTranscriptionTarget
		}
		prompt := "Transcribe the attached audio verbatim. Return plain text only, no commentary."
		decls := []any{map[string]any{
			"key":          "utterance.wav",
			"content_type": "audio/wav",
			"kind":         "voice",
		}}
		jobID, status, errType, code, message, ok := s.createFacadeJob(r, openAIFacadeRequest{Model: utteranceTarget}, utteranceTarget, nil, prompt, decls, "")
		if !ok {
			s.writeError(w, r, status, apiError{Code: code, Category: "voice", Message: message, Retryable: status >= 500})
			_ = errType
			return
		}
		reserved, err := s.voice.Reserve(ctx, voice.ReserveRequest{
			SessionID: newVoiceSessionID(now),
			TenantID:  tenantID,
			AppID:     appID,
			Target:    utteranceTarget,
			Mode:      voice.ModeUtterance,
			JobID:     jobID,
			LeaseTTL:  ttl,
			Now:       now,
		})
		if s.mapVoiceStoreError(w, r, err) {
			return
		}
		s.writeJSON(w, http.StatusCreated, map[string]any{
			"kind":       "voice_session",
			"session_id": reserved.ID,
			"target":     reserved.Target,
			"mode":       voice.ModeUtterance,
			"job_id":     jobID,
			"status":     string(reserved.Status),
			"session":    reserved,
			"next":       fmt.Sprintf("PUT the audio to /v1/jobs/%s/artifacts/utterance.wav, then poll GET /v1/jobs/%s; delete the session when done", jobID, jobID),
		})
		return
	}

	// Budgets (active leases and queued backlog per tenant) are enforced INSIDE
	// the store's admission transaction, so concurrent creates and replicas
	// cannot overshoot them: with the active budget spent a session can only
	// queue, and a spent queue budget is an explicit, retryable overload.
	// A browser with a running job is not offered: the session queues instead.
	placements, settle := s.voicePlacements(ctx, tenantID, target, req.IdentityRef)
	reserved, err := s.voice.Reserve(ctx, voice.ReserveRequest{
		SessionID:  newVoiceSessionID(now),
		TenantID:   tenantID,
		AppID:      appID,
		Target:     target,
		Placements: placements,
		MaxActive:  s.voiceMaxSessionsPerTenant,
		MaxQueued:  s.voiceMaxQueuedPerTenant,
		LeaseTTL:   ttl,
		Now:        now,
	})
	if nodeID := settle(reserved); err == nil && nodeID != "" {
		if reserved, err = s.bindVoiceNode(ctx, reserved, nodeID, ttl, now); err != nil {
			s.voiceMediaUnavailable(w, r)
			return
		}
	}
	if errors.Is(err, voice.ErrQueueFull) {
		s.voiceOverloaded(w, r, "UBAG-VOICE-QUEUE-FULL-004",
			fmt.Sprintf("voice-session queue budget reached (%d)", s.voiceMaxQueuedPerTenant))
		return
	}
	if s.mapVoiceStoreError(w, r, err) {
		return
	}
	status := http.StatusCreated
	if reserved.Status == voice.StatusQueued {
		status = http.StatusAccepted
	}
	s.writeJSON(w, status, map[string]any{
		"kind":       "voice_session",
		"session_id": reserved.ID,
		"target":     reserved.Target,
		"status":     string(reserved.Status),
		"session":    reserved,
	})
}

// voiceOverloaded answers an admission overload with explicit retry guidance
// (both the header and the structured retry_after_ms).
func (s *Server) voiceOverloaded(w http.ResponseWriter, r *http.Request, code, message string) {
	retry := s.voiceLeaseTTL()
	w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
	s.voiceError(w, r, http.StatusTooManyRequests, code, message, true, ptrInt(int(retry.Milliseconds())))
}

func (s *Server) handleVoiceSessionList(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		s.voiceError(w, r, http.StatusNotImplemented, "UBAG-VOICE-NOT-CONFIGURED-002",
			"voice sessions are not configured on this gateway", false, nil)
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	limit, ok := s.parseLimit(w, r, r.URL.Query().Get("limit"), 100)
	if !ok {
		return
	}
	tenantID, _ := requestScope(r)
	sessions, err := s.voice.List(r.Context(), tenantID, strings.TrimSpace(r.URL.Query().Get("target")), limit)
	if s.mapVoiceStoreError(w, r, err) {
		return
	}
	if sessions == nil {
		sessions = []voice.Session{}
	}
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "voice_sessions",
		Data:       voiceSessionsToData(sessions),
		TraceID:    traceIDFromContext(r.Context()),
	})
}

// voiceSessionsToData converts store sessions into collection rows. The
// Session struct is the wire shape; re-marshalling keeps that single source
// of truth without exporting a second DTO.
func voiceSessionsToData(sessions []voice.Session) []map[string]any {
	out := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		raw, err := json.Marshal(session)
		if err != nil {
			continue
		}
		row := map[string]any{}
		if err := json.Unmarshal(raw, &row); err != nil {
			continue
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) handleVoiceSessionSubtree(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		s.voiceError(w, r, http.StatusNotImplemented, "UBAG-VOICE-NOT-CONFIGURED-002",
			"voice sessions are not configured on this gateway", false, nil)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/voice/sessions/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		s.writeNotFound(w, r)
		return
	}
	sessionID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if action == "" {
		switch r.Method {
		case http.MethodGet:
			s.handleVoiceSessionGet(w, r, sessionID)
		case http.MethodDelete:
			s.handleVoiceSessionTerminate(w, r, sessionID)
		default:
			s.writeMethodNotAllowed(w, r, http.MethodGet)
		}
		return
	}
	if len(parts) > 2 {
		s.writeNotFound(w, r)
		return
	}
	switch action {
	case "connect", "mute", "renew", "terminate":
		// Every action mutates state: enforce the documented method BEFORE
		// any side effect (a GET with a body must never open media).
		if r.Method != http.MethodPost {
			s.writeMethodNotAllowed(w, r, http.MethodPost)
			return
		}
	default:
		s.writeNotFound(w, r)
		return
	}
	switch action {
	case "connect":
		s.handleVoiceSessionConnect(w, r, sessionID)
	case "mute":
		s.handleVoiceSessionMute(w, r, sessionID)
	case "renew":
		s.handleVoiceSessionRenew(w, r, sessionID)
	case "terminate":
		s.handleVoiceSessionTerminate(w, r, sessionID)
	}
}

// voiceSessionOrWrite loads the session enforcing tenant scoping; a session
// from another tenant is indistinguishable from a nonexistent one.
func (s *Server) voiceSessionOrWrite(w http.ResponseWriter, r *http.Request, sessionID string) (voice.Session, bool) {
	tenantID, _ := requestScope(r)
	session, found, err := s.voice.Get(r.Context(), tenantID, sessionID)
	if err != nil {
		_ = s.mapVoiceStoreError(w, r, err)
		return voice.Session{}, false
	}
	if !found {
		s.voiceError(w, r, http.StatusNotFound, "UBAG-VOICE-SESSION-NOT-FOUND-006",
			"voice session does not exist in this tenant", false, nil)
		return voice.Session{}, false
	}
	return session, true
}

func (s *Server) handleVoiceSessionGet(w http.ResponseWriter, r *http.Request, sessionID string) {
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	session, ok := s.voiceSessionOrWrite(w, r, sessionID)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"kind": "voice_session", "session": session})
}

func (s *Server) handleVoiceSessionConnect(w http.ResponseWriter, r *http.Request, sessionID string) {
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}
	if s.voiceMedia == nil {
		s.voiceError(w, r, http.StatusNotImplemented, "UBAG-VOICE-NOT-CONFIGURED-002",
			"voice media plane is not configured on this gateway", false, nil)
		return
	}
	var req voiceSessionConnectRequest
	if !s.decodeVoiceJSON(w, r, &req, 128<<10) { // SDP is text; bounded far above realistic size
		return
	}
	if strings.TrimSpace(req.SDPOffer) == "" {
		s.voiceError(w, r, http.StatusBadRequest, "UBAG-VOICE-SESSION-STATE-005",
			"sdp_offer is required", false, nil)
		return
	}
	tenantID, _ := requestScope(r)
	ctx := r.Context()
	now := time.Now().UTC()

	session, ok := s.voiceSessionOrWrite(w, r, sessionID)
	if !ok {
		return
	}
	switch session.Status {
	case voice.StatusQueued:
		// Promote: claim the first free account+environment now that the
		// caller is actually connecting. A conflict here is an honest
		// overload answer: the session stays queued, nothing is held.
		placements, settle := s.voicePlacements(ctx, tenantID, session.Target, "")
		claimed, err := s.voice.Claim(ctx, voice.ClaimRequest{
			TenantID:   tenantID,
			SessionID:  sessionID,
			Placements: placements,
			MaxActive:  s.voiceMaxSessionsPerTenant,
			LeaseTTL:   s.voiceLeaseTTL(),
			Now:        now,
		})
		if nodeID := settle(claimed); err == nil && nodeID != "" {
			if claimed, err = s.bindVoiceNode(ctx, claimed, nodeID, s.voiceLeaseTTL(), now); err != nil {
				s.voiceMediaUnavailable(w, r)
				return
			}
		}
		if errors.Is(err, voice.ErrConflict) {
			s.voiceError(w, r, http.StatusConflict, "UBAG-VOICE-SESSION-STATE-005",
				"no provider account or browser environment is free; the session remains queued", true, ptrInt(int(time.Second.Milliseconds())))
			return
		}
		if s.mapVoiceStoreError(w, r, err) {
			return
		}
		session = claimed
	case voice.StatusConnecting, voice.StatusConnected:
		// Re-connect (ICE restart): allowed while the lease is alive.
	default:
		s.voiceError(w, r, http.StatusConflict, "UBAG-VOICE-SESSION-STATE-005",
			"terminated voice sessions cannot connect", false, nil)
		return
	}
	if !session.LeaseExpires.IsZero() && session.LeaseExpires.Before(now) {
		_ = s.voice.Terminate(ctx, tenantID, sessionID, now, "lease_expired")
		s.releaseVoiceNodeHolds(ctx)
		s.voiceError(w, r, http.StatusConflict, "UBAG-VOICE-SESSION-STATE-005",
			"voice session lease has expired", true, nil)
		return
	}
	if session.NodeID != "" && !hostsNodeSessions(s.voiceMedia) {
		// The call is placed on a Helper Node; only a negotiator that terminates
		// media there may serve it. The primary's hub would resolve a relay for the
		// session's environment on the primary, which is some other account's browser.
		slog.Warn("voice session is bound to a helper node but the media plane cannot host node sessions", "session_id", sessionID)
		s.voiceMediaUnavailable(w, r)
		return
	}

	answer, err := s.voiceMedia.HandleOffer(ctx, session, req.SDPOffer)
	if err != nil {
		// Diagnostics stay server-side: internal addresses and implementation
		// detail never reach the client.
		slog.Warn("voice media negotiation failed", "session_id", sessionID, "error", err)
		s.voiceMediaUnavailable(w, r)
		return
	}
	// A negotiated connection is a real appointment: refresh the lease so
	// the sweeper cannot reap the session mid-handshake. A failed renewal
	// means the lease is gone — tear the media down rather than hand out an
	// answer for a session that no longer owns its resources.
	if err := s.voice.RenewLease(ctx, tenantID, sessionID, now.Add(s.voiceLeaseTTL()), now); err != nil {
		s.dropVoiceMedia(sessionID)
		if errors.Is(err, voice.ErrNotFound) || errors.Is(err, voice.ErrConflict) {
			s.voiceError(w, r, http.StatusConflict, "UBAG-VOICE-SESSION-STATE-005",
				"voice session lease is no longer held", true, nil)
			return
		}
		slog.Error("voice lease renewal failed", "session_id", sessionID, "error", err)
		s.writeError(w, r, http.StatusInternalServerError, internalError("voice session store error"))
		return
	}
	s.beginVoiceActivation(session)
	expires := now.Add(voiceMediaCredentialTTL)
	var iceServers []voice.ICEServer
	if provider, ok := s.voiceMedia.(interface {
		ClientICEServers(voice.Session) []voice.ICEServer
	}); ok {
		iceServers = provider.ClientICEServers(session)
	}
	s.writeJSON(w, http.StatusOK, voiceSessionConnectResponse{
		ICEServers:      iceServers,
		SessionID:       sessionID,
		Status:          string(voice.StatusConnecting),
		SDPAnswer:       answer,
		MediaCredential: issueVoiceMediaCredential(s.appSecret, session, expires),
		MediaExpiresMS:  expires.UnixMilli(),
		Kind:            "voice_session_connection",
	})
}

// voiceMediaUnavailable is the one answer for every media-plane refusal: the
// reason (an address, a node, an implementation detail) stays in the log.
func (s *Server) voiceMediaUnavailable(w http.ResponseWriter, r *http.Request) {
	s.voiceError(w, r, http.StatusServiceUnavailable, "UBAG-VOICE-MEDIA-UNAVAILABLE-007",
		"media plane cannot accept this connection", true, ptrInt(1000))
}

// voiceLeaseTTL is the configured lease window applied at claim and on every
// renewal. It is deliberately NOT derived from the session's remaining
// lifetime (which shrinks toward zero); ttl_seconds on create sets only the
// first lease window.
func (s *Server) voiceLeaseTTL() time.Duration {
	if s.voiceSessionTTL > 0 {
		return s.voiceSessionTTL
	}
	return defaultVoiceSessionTTL
}

// dropVoiceMedia closes any live media connection for the session.
func (s *Server) dropVoiceMedia(sessionID string) {
	if dropper, ok := s.voiceMedia.(interface{ Disconnect(sessionID string) }); ok {
		dropper.Disconnect(sessionID)
	}
}

func (s *Server) handleVoiceSessionMute(w http.ResponseWriter, r *http.Request, sessionID string) {
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}
	var req voiceSessionMuteRequest
	if !s.decodeVoiceJSON(w, r, &req, 4<<10) {
		return
	}
	if req.Muted == nil {
		s.voiceError(w, r, http.StatusBadRequest, "UBAG-VOICE-SESSION-STATE-005",
			"muted (bool) is required", false, nil)
		return
	}
	if _, ok := s.voiceSessionOrWrite(w, r, sessionID); !ok {
		return
	}
	tenantID, _ := requestScope(r)
	if err := s.voice.SetMuted(r.Context(), tenantID, sessionID, *req.Muted, time.Now().UTC()); s.mapVoiceStoreError(w, r, err) {
		return
	}
	// The mute flag must govern the microphone path itself, not just the
	// record: tell the live media connection (best effort — a session with
	// no media yet picks the flag up at connect).
	if media, ok := s.voiceMedia.(interface {
		SetMuted(sessionID string, muted bool) bool
	}); ok {
		media.SetMuted(sessionID, *req.Muted)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "muted": *req.Muted, "kind": "voice_session_control"})
}

func (s *Server) handleVoiceSessionRenew(w http.ResponseWriter, r *http.Request, sessionID string) {
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}
	if _, ok := s.voiceSessionOrWrite(w, r, sessionID); !ok {
		return
	}
	tenantID, _ := requestScope(r)
	now := time.Now().UTC()
	until := now.Add(s.voiceLeaseTTL())
	if err := s.voice.RenewLease(r.Context(), tenantID, sessionID, until, now); err != nil {
		// An expired or released lease cannot be revived: surface the state
		// conflict, never a silent success.
		if errors.Is(err, voice.ErrNotFound) {
			err = voice.ErrConflict
		}
		_ = s.mapVoiceStoreError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"session_id":       sessionID,
		"kind":             "voice_session_control",
		"lease_expires_at": until,
	})
}

func (s *Server) handleVoiceSessionTerminate(w http.ResponseWriter, r *http.Request, sessionID string) {
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}
	if _, ok := s.voiceSessionOrWrite(w, r, sessionID); !ok {
		return
	}
	tenantID, _ := requestScope(r)
	// Termination is idempotent in the store, and the media path drops
	// immediately — cleanup never waits for ICE to notice the session died.
	// With the terminating hold on, the leases outlive the record until the
	// provider confirms deactivation (see terminateVoiceSession).
	if err := s.terminateVoiceSession(r.Context(), tenantID, sessionID, time.Now().UTC(), "terminated_by_client"); s.mapVoiceStoreError(w, r, err) {
		return
	}
	s.dropVoiceMedia(sessionID)
	s.releaseVoiceNodeHolds(r.Context())
	s.writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "status": "terminated", "kind": "voice_session"})
}

// decodeVoiceJSON reads a bounded JSON body into out for voice mutations.
func (s *Server) decodeVoiceJSON(w http.ResponseWriter, r *http.Request, out any, limit int64) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		s.voiceError(w, r, http.StatusRequestEntityTooLarge, "UBAG-VOICE-SESSION-STATE-005",
			fmt.Sprintf("request body exceeds %d bytes", limit), false, nil)
		return false
	}
	if err != nil || len(strings.TrimSpace(string(body))) == 0 {
		s.voiceError(w, r, http.StatusBadRequest, "UBAG-VOICE-SESSION-STATE-005",
			"request body must be JSON", false, nil)
		return false
	}
	if err := json.Unmarshal(body, out); err != nil {
		s.voiceError(w, r, http.StatusBadRequest, "UBAG-VOICE-SESSION-STATE-005",
			"request body is not valid JSON for this endpoint", false, nil)
		return false
	}
	return true
}

// issueVoiceMediaCredential derives a short-lived media credential scoped to
// ONE session: "voice-media|<tenant>|<app>|<session>|<expiryUnix>|<hmac>".
// It authorizes the client's control data channel (mute, ping): the channel
// ignores every command until it presents a credential that
// VerifyVoiceMediaCredential accepts for exactly this tenant, app, session and
// a not-yet-expired time. The media itself is protected separately by the
// authenticated signaling exchange and DTLS.
func issueVoiceMediaCredential(appSecret string, session voice.Session, expires time.Time) string {
	return voice.IssueMediaCredential([]byte(appSecret), session, expires)
}

// VerifyVoiceMediaCredential validates a credential against the session it
// must belong to. A credential minted for another tenant, app or session, or
// an expired or altered one, fails closed.
func VerifyVoiceMediaCredential(appSecret string, session voice.Session, credential string, now time.Time) bool {
	return voice.VerifyMediaCredential([]byte(appSecret), session, credential, now)
}

// VoiceControlAuthorizer returns the hook the media hub uses to authorize a
// control-channel auth message for a session.
func (s *Server) VoiceControlAuthorizer() func(voice.Session, string) bool {
	return func(session voice.Session, credential string) bool {
		return VerifyVoiceMediaCredential(s.appSecret, session, credential, time.Now().UTC())
	}
}
