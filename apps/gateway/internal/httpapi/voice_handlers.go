package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
	Kind            string `json:"kind"`
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

// voicePlacements resolves, server-side, the (provider account, browser
// environment) pairs this tenant may hold for the target: an authenticated
// topology context paired with the usable instance that actually hosts it.
// A caller-supplied identity_ref is only a PREFERENCE among these pairs — it
// is never proof of ownership, so naming an account the tenant does not own
// (or one without a hosting environment) resolves to nothing.
func (s *Server) voicePlacements(ctx context.Context, tenantID, target, preferred string) []voice.Placement {
	if s.topology == nil {
		return nil
	}
	instances, err := s.topology.ListInstances(ctx, topology.InstanceFilter{TenantID: tenantID, Limit: 100})
	if err != nil {
		return nil
	}
	usable := map[string]bool{}
	for _, instance := range instances {
		switch strings.ToLower(strings.TrimSpace(instance.State)) {
		case "failed", "unhealthy", "draining", "recycling", "stopped", "terminated":
		default:
			if id := strings.TrimSpace(instance.InstanceID); id != "" {
				usable[id] = true
			}
		}
	}
	contexts, err := s.topology.ListContexts(ctx, topology.ContextFilter{TenantID: tenantID, Limit: 1000})
	if err != nil {
		return nil
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
	return out
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
	reserved, err := s.voice.Reserve(ctx, voice.ReserveRequest{
		SessionID:  newVoiceSessionID(now),
		TenantID:   tenantID,
		AppID:      appID,
		Target:     target,
		Placements: s.voicePlacements(ctx, tenantID, target, req.IdentityRef),
		MaxActive:  s.voiceMaxSessionsPerTenant,
		MaxQueued:  s.voiceMaxQueuedPerTenant,
		LeaseTTL:   ttl,
		Now:        now,
	})
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
		claimed, err := s.voice.Claim(ctx, voice.ClaimRequest{
			TenantID:   tenantID,
			SessionID:  sessionID,
			Placements: s.voicePlacements(ctx, tenantID, session.Target, ""),
			MaxActive:  s.voiceMaxSessionsPerTenant,
			LeaseTTL:   s.voiceLeaseTTL(),
			Now:        now,
		})
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
		s.voiceError(w, r, http.StatusConflict, "UBAG-VOICE-SESSION-STATE-005",
			"voice session lease has expired", true, nil)
		return
	}

	answer, err := s.voiceMedia.HandleOffer(ctx, session, req.SDPOffer)
	if err != nil {
		// Diagnostics stay server-side: internal addresses and implementation
		// detail never reach the client.
		slog.Warn("voice media negotiation failed", "session_id", sessionID, "error", err)
		s.voiceError(w, r, http.StatusServiceUnavailable, "UBAG-VOICE-MEDIA-UNAVAILABLE-007",
			"media plane cannot accept this connection", true, ptrInt(1000))
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
	s.writeJSON(w, http.StatusOK, voiceSessionConnectResponse{
		SessionID:       sessionID,
		Status:          string(voice.StatusConnecting),
		SDPAnswer:       answer,
		MediaCredential: issueVoiceMediaCredential(s.appSecret, sessionID, expires),
		MediaExpiresMS:  expires.UnixMilli(),
		Kind:            "voice_session_connection",
	})
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
	if err := s.voice.Terminate(r.Context(), tenantID, sessionID, time.Now().UTC(), "terminated_by_client"); s.mapVoiceStoreError(w, r, err) {
		return
	}
	s.dropVoiceMedia(sessionID)
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

// issueVoiceMediaCredential derives a short-lived, session-scoped media
// token: "voice-media|<session>|<expiryUnix>|<hmac>". The token carries no
// tenant or account data and grants nothing beyond this one session's media
// path; VerifyVoiceMediaCredential validates it against the shared secret.
func issueVoiceMediaCredential(appSecret, sessionID string, expires time.Time) string {
	payload := fmt.Sprintf("voice-media|%s|%d", sessionID, expires.Unix())
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyVoiceMediaCredential validates a credential issued at connect. The
// media plane calls this before accepting any client media traffic; an
// expired or foreign-session credential fails closed.
func VerifyVoiceMediaCredential(appSecret, sessionID, credential string, now time.Time) bool {
	parts := strings.Split(credential, "|")
	if len(parts) != 4 || parts[0] != "voice-media" || parts[1] != sessionID {
		return false
	}
	expiresUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.Unix() > expiresUnix {
		return false
	}
	expected := issueVoiceMediaCredential(appSecret, sessionID, time.Unix(expiresUnix, 0))
	return hmac.Equal([]byte(expected), []byte(credential))
}
