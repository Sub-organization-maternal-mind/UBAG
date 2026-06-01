package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/ratelimit"
)

type rateLimitPolicyPayload struct {
	Action        string `json:"action"`
	Limit         int    `json:"limit"`
	WindowSeconds int    `json:"window_seconds"`
	Burst         int    `json:"burst,omitempty"`
}

type rateLimitStatusResponse struct {
	APIVersion string                   `json:"api_version"`
	Enabled    bool                     `json:"enabled"`
	Policies   []rateLimitPolicyPayload `json:"policies"`
	TraceID    string                   `json:"trace_id"`
}

// notImplementedError builds a 501 envelope used when an optional enterprise
// component is not configured on this gateway.
func notImplementedError(message string) apiError {
	return apiError{
		Code:      "UBAG-NOT-IMPLEMENTED-001",
		Category:  "internal",
		Message:   message,
		Retryable: false,
	}
}

func (s *Server) writeNotImplemented(w http.ResponseWriter, r *http.Request, message string) {
	s.writeError(w, r, http.StatusNotImplemented, notImplementedError(message))
}

// requireIdempotencyKey extracts and validates the Idempotency-Key header for
// mutating enterprise routes. It writes the appropriate 400 and returns ok=false
// when the key is missing or malformed.
func (s *Server) requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	if key == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-MISSING-001", "Idempotency-Key is required for this operation"))
		return "", false
	}
	if !isIdempotencyKey(key) {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-IDEMPOTENCY-KEY-001", "Idempotency-Key must be 16-128 characters and contain only letters, numbers, dot, underscore, colon, or dash"))
		return "", false
	}
	return key, true
}

// rateLimitEnforced reports whether rate limiting is fully configured.
func (s *Server) rateLimitEnforced() bool {
	return s.rateLimitEnabled && s.rateLimiter != nil && s.rateResolver != nil
}

// policyLimiter is the optional capability used to apply a per-action policy.
type policyLimiter interface {
	AllowPolicy(ctx context.Context, key string, policy ratelimit.Policy, cost int) (ratelimit.Decision, error)
}

// withRateLimit enforces per-action rate limits. It is a pass-through unless
// rate limiting is enabled and a principal is present (i.e. an authenticated
// route). Public routes that skip auth never carry a principal and are exempt.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.rateLimitEnforced() {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := principalFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		action := rateLimitAction(r.Method, r.URL.Path)
		key := principal.TenantID + ":" + principal.AppID + ":" + action
		policy := s.rateResolver.Resolve(action)

		var (
			decision ratelimit.Decision
			err      error
		)
		if pl, hasPolicy := s.rateLimiter.(policyLimiter); hasPolicy {
			decision, err = pl.AllowPolicy(r.Context(), key, policy, 1)
		} else {
			decision, err = s.rateLimiter.Allow(r.Context(), key, 1)
		}
		if err != nil {
			// Fail open: a limiter backend error must not take down the API.
			next.ServeHTTP(w, r)
			return
		}

		// Blueprint §10.6: IETF draft-ietf-httpapi-ratelimit-headers (RateLimit-*, not X-RateLimit-*).
		w.Header().Set("RateLimit-Limit", strconv.Itoa(decision.Limit))
		w.Header().Set("RateLimit-Remaining", strconv.Itoa(decision.Remaining))
		if !decision.ResetAt.IsZero() {
			w.Header().Set("RateLimit-Reset", strconv.FormatInt(decision.ResetAt.UTC().Unix(), 10))
		}

		if !decision.Allowed {
			retryAfter := decision.RetryAfter
			if retryAfter <= 0 && !decision.ResetAt.IsZero() {
				retryAfter = time.Until(decision.ResetAt)
			}
			seconds := int(retryAfter.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			retryMS := seconds * 1000
			s.writeError(w, r, http.StatusTooManyRequests, apiError{
				Code:         "UBAG-RATE-APP-001",
				Category:     "rate",
				Message:      "rate limit exceeded for this action",
				Retryable:    true,
				RetryAfterMS: &retryMS,
				Details: map[string]any{
					"action":    action,
					"limit":     decision.Limit,
					"remaining": decision.Remaining,
				},
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// rateLimitAction maps an HTTP method + path onto a stable action string that
// the PolicyResolver understands. Unknown routes fall back to the resolver's
// default policy via a synthesized action label.
func rateLimitAction(method, path string) string {
	p := routePattern(path)
	switch {
	case method == http.MethodPost && p == "/v1/jobs":
		return "job:create"
	case method == http.MethodPost && p == "/v1/jobs/{job_id}/cancel":
		return "job:cancel"
	case method == http.MethodPost && p == "/v1/jobs/{job_id}/retry":
		return "job:retry"
	case method == http.MethodGet && p == "/v1/jobs":
		return "job:list"
	case method == http.MethodGet:
		return "job:read"
	default:
		return method + " " + p
	}
}

// handleRateLimits exposes the configured policies and is gated behind the
// rate_limit:manage RBAC action.
func (s *Server) handleRateLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if !s.authorizeGatewayAction(w, r, "rate_limit:manage") {
		return
	}
	if s.rateResolver == nil {
		s.writeJSON(w, http.StatusOK, rateLimitStatusResponse{
			APIVersion: s.apiVersion,
			Enabled:    false,
			Policies:   []rateLimitPolicyPayload{},
			TraceID:    traceIDFromContext(r.Context()),
		})
		return
	}

	policies := s.rateResolver.Policies()
	actions := make([]string, 0, len(policies))
	for action := range policies {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	payload := make([]rateLimitPolicyPayload, 0, len(actions))
	for _, action := range actions {
		policy := policies[action]
		payload = append(payload, rateLimitPolicyPayload{
			Action:        action,
			Limit:         policy.Limit,
			WindowSeconds: int(policy.Window.Seconds()),
			Burst:         policy.Burst,
		})
	}
	s.writeJSON(w, http.StatusOK, rateLimitStatusResponse{
		APIVersion: s.apiVersion,
		Enabled:    s.rateLimitEnforced(),
		Policies:   payload,
		TraceID:    traceIDFromContext(r.Context()),
	})
}
