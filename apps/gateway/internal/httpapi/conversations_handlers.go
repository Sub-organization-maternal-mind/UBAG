package httpapi

import (
	"net/http"

	"github.com/ubag/ubag/apps/gateway/internal/conversations"
)

// conversationListResponse is the typed GET /v1/conversations envelope. It
// mirrors the OpenAPI ConversationListResponse (api_version + conversations[] +
// next_cursor), deliberately avoiding the closed collectionResponse.kind enum.
// The conversations.Conversation JSON tags already match the response item
// schema field-for-field, so records are emitted directly.
type conversationListResponse struct {
	APIVersion    string                       `json:"api_version"`
	Conversations []conversations.Conversation `json:"conversations"`
	NextCursor    *string                      `json:"next_cursor"`
}

// handleConversations serves GET /v1/conversations, listing the conversation
// thread bindings for the authenticated tenant/app. It returns 501 when the
// conversation subsystem is not configured (UBAG_CONVERSATIONS_ENABLED off),
// mirroring the /v1/alerts list handler's nil-safe posture.
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.conversations == nil {
		s.writeNotImplemented(w, r, "conversation subsystem is not configured")
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}

	query := r.URL.Query()
	limit, ok := s.parseLimit(w, r, query.Get("limit"), 50)
	if !ok {
		return
	}

	tenantID, appID := requestScope(r)
	records, err := s.conversations.List(r.Context(), conversations.Filter{
		TenantID: tenantID,
		AppID:    appID,
		Limit:    limit,
	})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list conversations"))
		return
	}
	if records == nil {
		records = []conversations.Conversation{}
	}

	s.writeJSON(w, http.StatusOK, conversationListResponse{
		APIVersion:    s.apiVersion,
		Conversations: records,
		NextCursor:    nil,
	})
}
