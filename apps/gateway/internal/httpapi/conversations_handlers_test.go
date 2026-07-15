package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/conversations"
)

// seededConversationManager builds an in-memory conversations.Manager pre-loaded
// with convs, mirroring newTestAlertManager's role for the alert routes.
func seededConversationManager(t *testing.T, convs ...conversations.Conversation) *conversations.Manager {
	t.Helper()
	store := conversations.NewMemoryStore()
	for _, conv := range convs {
		if _, err := store.Bind(context.Background(), conv); err != nil {
			t.Fatalf("seed conversation: %v", err)
		}
	}
	return conversations.NewManager(store, nil, "memory")
}

func TestConversationsListReturns501WhenDisabled(t *testing.T) {
	// Config.Conversations == nil must yield 501, never a panic.
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin"}).Handler()
	resp := doJSON(server, http.MethodGet, "/v1/conversations", "", authHeaders(""))
	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("GET /v1/conversations = %d, want 501; body=%s", resp.Code, resp.Body.String())
	}
}

func TestConversationsListIsTenantScoped(t *testing.T) {
	// A caller from tenant_a must not see tenant_b's conversations.
	now := time.Unix(1, 0).UTC()
	manager := seededConversationManager(t,
		conversations.Conversation{
			TenantID: "tenant_a", AppID: "app_a", Target: "mock", ConversationKey: "c-a",
			ProviderThreadRef: "https://example/chat/a", State: conversations.StateActive,
			CreatedAt: now, LastUsedAt: now,
		},
		conversations.Conversation{
			TenantID: "tenant_b", AppID: "app_a", Target: "mock", ConversationKey: "c-b",
			ProviderThreadRef: "https://example/chat/b", State: conversations.StateActive,
			CreatedAt: now, LastUsedAt: now,
		},
	)
	server := NewServer(Config{
		AppSecret: "dev-secret", ActorRole: "viewer",
		TenantID: "tenant_a", AppID: "app_a", Conversations: manager,
	}).Handler()

	resp := doJSON(server, http.MethodGet, "/v1/conversations", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("list = %d; body=%s", resp.Code, resp.Body.String())
	}
	var listResp struct {
		APIVersion    string           `json:"api_version"`
		Conversations []map[string]any `json:"conversations"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listResp.Conversations) != 1 {
		t.Fatalf("len(conversations) = %d, want 1 (tenant scoped); body=%s", len(listResp.Conversations), resp.Body.String())
	}
	if listResp.Conversations[0]["conversation_key"] != "c-a" {
		t.Fatalf("conversation_key = %v, want c-a", listResp.Conversations[0]["conversation_key"])
	}
	if listResp.Conversations[0]["tenant_id"] != "tenant_a" {
		t.Fatalf("tenant_id = %v, want tenant_a", listResp.Conversations[0]["tenant_id"])
	}
}

func TestConversationsListRequiresJobRead(t *testing.T) {
	// A principal lacking job:read gets 403. Every named role holds job:read, so
	// an unknown role exercises the RBAC default-deny branch.
	server := NewServer(Config{
		AppSecret: "dev-secret", ActorRole: "guest",
		TenantID: "tenant_a", AppID: "app_a", Conversations: seededConversationManager(t),
	}).Handler()
	resp := doJSON(server, http.MethodGet, "/v1/conversations", "", authHeaders(""))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("guest list = %d, want 403; body=%s", resp.Code, resp.Body.String())
	}
}

func TestConversationsListPaginates(t *testing.T) {
	// Mirror the /v1/alerts pagination assertions: limit truncates the result and
	// an out-of-range limit is a 400.
	now := time.Unix(1, 0).UTC()
	seed := make([]conversations.Conversation, 0, 3)
	for i, key := range []string{"c1", "c2", "c3"} {
		seed = append(seed, conversations.Conversation{
			TenantID: "tenant_a", AppID: "app_a", Target: "mock", ConversationKey: key,
			ProviderThreadRef: "https://example/chat/" + key, State: conversations.StateActive,
			CreatedAt: now, LastUsedAt: now.Add(time.Duration(i) * time.Second),
		})
	}
	server := NewServer(Config{
		AppSecret: "dev-secret", ActorRole: "viewer",
		TenantID: "tenant_a", AppID: "app_a", Conversations: seededConversationManager(t, seed...),
	}).Handler()

	resp := doJSON(server, http.MethodGet, "/v1/conversations?limit=2", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("list = %d; body=%s", resp.Code, resp.Body.String())
	}
	var listResp struct {
		Conversations []map[string]any `json:"conversations"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listResp.Conversations) != 2 {
		t.Fatalf("len(conversations) = %d, want 2 (limit=2)", len(listResp.Conversations))
	}

	bad := doJSON(server, http.MethodGet, "/v1/conversations?limit=999", "", authHeaders(""))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("limit=999 = %d, want 400; body=%s", bad.Code, bad.Body.String())
	}
}
