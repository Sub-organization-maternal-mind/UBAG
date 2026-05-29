package httpapi

import (
	"net/http"
	"time"
)

type cacheStatsPayload struct {
	Entries int `json:"entries"`
	Hits    int `json:"hits"`
	Misses  int `json:"misses"`
}

type cacheStatusEnabledResponse struct {
	APIVersion string              `json:"api_version"`
	Profile    string              `json:"profile"`
	Enabled    bool                `json:"enabled"`
	TTLSeconds int                 `json:"ttl_seconds"`
	Stats      cacheStatsPayload   `json:"stats"`
	Entries    []cacheEntryPayload `json:"entries"`
	TraceID    string              `json:"trace_id"`
}

type cachePurgeResponse struct {
	APIVersion string `json:"api_version"`
	Status     string `json:"status"`
	Purged     int    `json:"purged"`
	TraceID    string `json:"trace_id"`
}

// cacheEntryPayload deliberately omits the cached payload Value: cache entries
// may contain provider responses and must never be exposed via the API.
type cacheEntryPayload struct {
	Key       string    `json:"key"`
	Target    string    `json:"target"`
	Command   string    `json:"command"`
	InputHash string    `json:"input_hash"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Server) getCacheStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}

	// Backward-compatible disabled response when no cache is configured.
	if s.responseCache == nil || !s.responseCache.Enabled() {
		s.writeJSON(w, http.StatusOK, cacheStatusResponse{
			APIVersion: s.apiVersion,
			Profile:    "edge",
			Enabled:    false,
			Entries:    []any{},
			TraceID:    traceIDFromContext(r.Context()),
		})
		return
	}

	tenantID, appID := requestScope(r)
	limit, ok := s.parseLimit(w, r, r.URL.Query().Get("limit"), 50)
	if !ok {
		return
	}
	stats, err := s.responseCache.Stats(r.Context(), tenantID, appID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read cache stats"))
		return
	}
	entries, err := s.responseCache.List(r.Context(), tenantID, appID, limit)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list cache entries"))
		return
	}
	payloadEntries := make([]cacheEntryPayload, 0, len(entries))
	for _, entry := range entries {
		payloadEntries = append(payloadEntries, cacheEntryPayload{
			Key:       entry.Key,
			Target:    entry.Target,
			Command:   entry.Command,
			InputHash: entry.InputHash,
			CreatedAt: entry.CreatedAt.UTC(),
			ExpiresAt: entry.ExpiresAt.UTC(),
		})
	}
	s.writeJSON(w, http.StatusOK, cacheStatusEnabledResponse{
		APIVersion: s.apiVersion,
		Profile:    "edge",
		Enabled:    true,
		TTLSeconds: int(s.responseCache.TTL().Seconds()),
		Stats: cacheStatsPayload{
			Entries: stats.Entries,
			Hits:    stats.Hits,
			Misses:  stats.Misses,
		},
		Entries: payloadEntries,
		TraceID: traceIDFromContext(r.Context()),
	})
}

func (s *Server) purgeCache(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "rate_limit:manage") {
		return
	}
	if s.responseCache == nil || !s.responseCache.Enabled() {
		s.writeNotImplemented(w, r, "response cache is not configured")
		return
	}
	tenantID, appID := requestScope(r)
	purged, err := s.responseCache.Purge(r.Context(), tenantID, appID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to purge cache"))
		return
	}
	s.writeJSON(w, http.StatusOK, cachePurgeResponse{
		APIVersion: s.apiVersion,
		Status:     "purged",
		Purged:     purged,
		TraceID:    traceIDFromContext(r.Context()),
	})
}
