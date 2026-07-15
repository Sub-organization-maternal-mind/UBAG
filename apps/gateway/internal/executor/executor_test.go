package executor

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/conversations"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// ---------------------------------------------------------------------------
// ProviderConfigFromModelSettings — the public model_settings contract is the
// same flat shape as the internal worker provider_config, so the transform is a
// copy that drops reserved control keys. These three tests are the Task B4
// acceptance surface.
// ---------------------------------------------------------------------------

func TestEnvelopeCopiesModelSettingsIntoProviderConfig(t *testing.T) {
	// model_settings is the public, catalog-validated contract; provider_config
	// is the internal worker protocol. They are the same flat shape by
	// construction, so this is a copy, not a translation.
	settings := map[string]any{"model": "mock-deep", "thinking": "extended", "deepthink": true}
	got := ProviderConfigFromModelSettings(settings)
	want := map[string]any{"model": "mock-deep", "thinking": "extended", "deepthink": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider_config = %#v, want %#v", got, want)
	}
}

func TestEnvelopeOmitsProviderConfigWhenNoModelSettings(t *testing.T) {
	if got := ProviderConfigFromModelSettings(nil); len(got) != 0 {
		t.Fatalf("provider_config = %#v, want empty so operator defaults apply", got)
	}
}

func TestProviderConfigDropsReservedKeys(t *testing.T) {
	// _enabled / _new_chat are reserved worker control keys that gate whole
	// phases of the interaction. Defense in depth: the schema and the validator
	// already block them, but the envelope must never carry one through.
	got := ProviderConfigFromModelSettings(map[string]any{"_enabled": false, "model": "mock-fast"})
	if _, ok := got["_enabled"]; ok {
		t.Fatal("reserved key _enabled leaked into provider_config")
	}
	if got["model"] != "mock-fast" {
		t.Fatalf("model = %v, want mock-fast to survive alongside a dropped reserved key", got["model"])
	}
}

// ---------------------------------------------------------------------------
// Envelope wiring — provider_config + conversation-block injection. These
// assert the seam that keeps the worker unchanged and the flag-off envelope
// byte-identical to today.
// ---------------------------------------------------------------------------

func TestEnvelopeFromJobIsByteIdenticalWithoutFeatures(t *testing.T) {
	// EnvelopeFromJob is the pre-feature entry point. With no model settings and
	// no conversation manager the enriched builder must produce exactly the same
	// envelope, so every existing dispatch caller is unaffected.
	job := jobstore.Job{
		ID: "job_1", APIVersion: "2026-05-22", TenantID: "t1", AppID: "a1",
		Target: "mock", CommandType: "submit",
		Input:   map[string]any{"prompt": "hi"},
		Options: map[string]any{"priority": "normal"},
	}
	base := EnvelopeFromJob(job)
	enriched := EnvelopeFromJobWithConversation(context.Background(), job, nil)
	if enriched.Conversation != nil {
		t.Fatal("conversation block must be omitted when the manager is nil")
	}
	if _, ok := enriched.Job.Options["provider_config"]; ok {
		t.Fatal("provider_config must be absent when no model_settings are supplied")
	}
	if !reflect.DeepEqual(base, enriched) {
		t.Fatalf("flag-off enriched envelope must equal the base envelope\nbase=%#v\nenriched=%#v", base, enriched)
	}
}

func TestEnvelopeInjectsProviderConfigFromModelSettings(t *testing.T) {
	// provider_config injection now happens at create time (the gateway writes the
	// validated model_settings into options.provider_config), so the builder no
	// longer takes model settings. This asserts the pure transform the gateway
	// uses: real settings survive, reserved control keys are dropped.
	pc := ProviderConfigFromModelSettings(map[string]any{"model": "mock-deep", "_new_chat": true})
	if pc["model"] != "mock-deep" {
		t.Fatalf("provider_config.model = %v, want mock-deep", pc["model"])
	}
	if _, leaked := pc["_new_chat"]; leaked {
		t.Fatal("reserved key _new_chat leaked into provider_config")
	}
}

func TestEnvelopeInjectsResolvedConversationThreadRef(t *testing.T) {
	ctx := context.Background()
	store := conversations.NewMemoryStore()
	now := time.Unix(1, 0).UTC()
	if _, err := store.Bind(ctx, conversations.Conversation{
		TenantID: "t1", AppID: "a1", Target: "mock", ConversationKey: "c1",
		ProviderThreadRef: "https://example/chat/1", State: conversations.StateActive,
		CreatedAt: now, LastUsedAt: now,
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	mgr := conversations.NewManager(store, nil, "memory")
	job := jobstore.Job{
		ID: "job_1", TenantID: "t1", AppID: "a1", Target: "mock", CommandType: "submit",
		ConversationID: "c1",
		Options:        map[string]any{"conversation_missing": "restart"},
	}
	env := EnvelopeFromJobWithConversation(ctx, job, mgr)
	if env.Conversation == nil {
		t.Fatal("conversation block must be present when conversation_id + manager are set")
	}
	if env.Conversation.Key != "c1" {
		t.Fatalf("conversation.key = %q, want c1", env.Conversation.Key)
	}
	if env.Conversation.ThreadRef != "https://example/chat/1" {
		t.Fatalf("conversation.thread_ref = %q, want the resolved chat URL", env.Conversation.ThreadRef)
	}
	if env.Conversation.OnMissing != "restart" {
		t.Fatalf("conversation.on_missing = %q, want restart from options", env.Conversation.OnMissing)
	}
}

func TestEnvelopeConversationUnboundKeyHasEmptyThreadRefAndDefaultOnMissing(t *testing.T) {
	ctx := context.Background()
	mgr := conversations.NewManager(conversations.NewMemoryStore(), nil, "memory")
	job := jobstore.Job{
		ID: "job_1", TenantID: "t1", AppID: "a1", Target: "mock", CommandType: "submit",
		ConversationID: "c-unbound",
	}
	env := EnvelopeFromJobWithConversation(ctx, job, mgr)
	if env.Conversation == nil {
		t.Fatal("conversation block must be present even when the key is not yet bound")
	}
	if env.Conversation.ThreadRef != "" {
		t.Fatalf("unbound conversation.thread_ref = %q, want empty", env.Conversation.ThreadRef)
	}
	if env.Conversation.OnMissing != "fail" {
		t.Fatalf("conversation.on_missing = %q, want the default fail", env.Conversation.OnMissing)
	}
}

func TestEnvelopeOmitsConversationWhenJobHasNoConversationID(t *testing.T) {
	ctx := context.Background()
	mgr := conversations.NewManager(conversations.NewMemoryStore(), nil, "memory")
	job := jobstore.Job{
		ID: "job_1", TenantID: "t1", AppID: "a1", Target: "mock", CommandType: "submit",
	}
	env := EnvelopeFromJobWithConversation(ctx, job, mgr)
	if env.Conversation != nil {
		t.Fatal("conversation block must be omitted when the job carries no conversation_id")
	}
}
