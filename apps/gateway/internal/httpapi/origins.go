package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Origin policy for the voice surface. Server-to-server clients send no
// Origin header and authenticate with their bearer token; a BROWSER always
// sends one, so for browsers the gateway only answers origins the operator
// allowed (UBAG_ALLOWED_ORIGINS, comma-separated exact origins such as
// https://app.example.com). With no allowlist configured a browser request is
// accepted only when it is same-origin with the gateway itself. "*" is never
// honoured: media connections carry live audio, so a wildcard would let any
// page a user visits drive their session.
func parseAllowedOrigins(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimRight(strings.TrimSpace(part), "/")
		if part != "" && part != "*" {
			out = append(out, part)
		}
	}
	return out
}

func allowedOriginsFromEnv() []string { return parseAllowedOrigins(os.Getenv("UBAG_ALLOWED_ORIGINS")) }

func (s *Server) originAllowed(r *http.Request, origin string) bool {
	for _, allowed := range s.allowedOrigins {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}
	if len(s.allowedOrigins) > 0 {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// withVoiceOriginPolicy enforces the origin policy on /v1/voice/* and answers
// CORS preflights for allowed origins.
func (s *Server) withVoiceOriginPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" || !strings.HasPrefix(r.URL.Path, "/v1/voice/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.originAllowed(r, origin) {
			s.writeError(w, r, http.StatusForbidden, authzError("UBAG-AUTH-ORIGIN-001", "origin is not allowed for voice sessions"))
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, Ubag-Api-Version")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
