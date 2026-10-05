package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

func TestCapabilitiesHandler(t *testing.T) {
	srv := NewServer(Config{
		AppSecret: "dev-secret",
		ActorRole: "service",
		Executor:  &recordingExecutor{},
		Topology:  topology.NewMemoryStore(),
	})
	handler := srv.Handler()
	rec := doJSON(handler, http.MethodGet, "/v1/capabilities", "", authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var response collectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Kind != "capabilities" {
		t.Fatalf("kind = %q", response.Kind)
	}
	byTarget := map[string]map[string]any{}
	for _, entry := range response.Data {
		if key, _ := entry["target"].(string); key != "" {
			byTarget[key] = entry
		}
	}
	for _, want := range []string{"chatgpt_web", "gemini_web", "deepseek_web", "duckai_web", "mistral_lechat", "mock"} {
		if _, ok := byTarget[want]; !ok {
			t.Fatalf("target %q missing from capabilities", want)
		}
	}

	chatgpt := byTarget["chatgpt_web"]
	voice, _ := chatgpt["voice"].(map[string]any)
	if voice["live"] != true {
		t.Fatalf("chatgpt voice.live = %v, want true", voice["live"])
	}
	if voice["utterance_jobs"] != true {
		t.Fatalf("chatgpt voice.utterance_jobs = %v, want true", voice["utterance_jobs"])
	}
	if voice["available_accounts"] != float64(0) {
		t.Fatalf("empty topology must advertise 0 accounts, got %v", voice["available_accounts"])
	}

	// Inline parts intersect with the attachment policy: chatgpt accepts both,
	// so both families must list members.
	parts, _ := chatgpt["inline_message_parts"].(map[string]any)
	images, _ := parts["image_url"].([]any)
	audio, _ := parts["input_audio"].([]any)
	if len(images) == 0 || len(audio) == 0 {
		t.Fatalf("chatgpt inline parts = %v", parts)
	}
	if parts["remote_urls"] != false {
		t.Fatalf("remote_urls must advertise false, got %v", parts["remote_urls"])
	}

	// DeepSeek accepts images but no audio at all, so inline audio must be
	// empty while images stay available.
	deepseek := byTarget["deepseek_web"]
	voice, _ = deepseek["voice"].(map[string]any)
	if voice["live"] != false {
		t.Fatalf("deepseek voice.live = %v, want false", voice["live"])
	}
	if voice["utterance_jobs"] != false {
		t.Fatalf("deepseek voice.utterance_jobs = %v, want false (no audio kinds accepted)", voice["utterance_jobs"])
	}
	parts, _ = deepseek["inline_message_parts"].(map[string]any)
	audio, _ = parts["input_audio"].([]any)
	if len(audio) != 0 {
		t.Fatalf("deepseek must not advertise inline audio, got %v", parts["input_audio"])
	}

	mock := byTarget["mock"]
	mockVoice, _ := mock["voice"].(map[string]any)
	if mockVoice["live"] != false || mockVoice["utterance_jobs"] != false {
		t.Fatalf("mock voice = %v", mockVoice)
	}
}

func TestCapabilitiesVoiceAccountsFromTopology(t *testing.T) {
	store := topology.NewMemoryStore()
	for _, id := range []string{"b1", "b2", "b3"} {
		store.AddInstance(topology.BrowserInstance{InstanceID: id, TenantID: "tenant_edge", State: "ready"})
	}
	store.AddContext(topology.ProviderContext{ContextID: "ctx_chatgpt_1", InstanceID: "b1", TenantID: "tenant_edge",
		TargetID: "chatgpt_web", IdentityRef: "acct-1", LoginState: "authenticated"})
	store.AddContext(topology.ProviderContext{ContextID: "ctx_chatgpt_2", InstanceID: "b2", TenantID: "tenant_edge",
		TargetID: "chatgpt_web", IdentityRef: "acct-2", LoginState: "login_required"})
	// Authenticated but with no hosting environment: not usable.
	store.AddContext(topology.ProviderContext{ContextID: "ctx_chatgpt_orphan", InstanceID: "missing", TenantID: "tenant_edge",
		TargetID: "chatgpt_web", IdentityRef: "acct-3", LoginState: "authenticated"})
	store.AddContext(topology.ProviderContext{ContextID: "ctx_gemini_1", InstanceID: "b3", TenantID: "tenant_edge",
		TargetID: "gemini_web", IdentityRef: "acct-g", LoginState: "authenticated"})
	srv := NewServer(Config{
		AppSecret: "dev-secret",
		ActorRole: "service",
		Executor:  &recordingExecutor{},
		Topology:  store,
	})
	handler := srv.Handler()
	rec := doJSON(handler, http.MethodGet, "/v1/capabilities", "", authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var response collectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	accounts := map[string]int{}
	for _, entry := range response.Data {
		key, _ := entry["target"].(string)
		voice, _ := entry["voice"].(map[string]any)
		n, _ := voice["available_accounts"].(float64)
		accounts[key] = int(n)
	}
	if accounts["chatgpt_web"] != 1 {
		t.Fatalf("chatgpt accounts = %d, want 1 (login_required and host-less contexts excluded)", accounts["chatgpt_web"])
	}
	if accounts["gemini_web"] != 1 {
		t.Fatalf("gemini accounts = %d, want 1", accounts["gemini_web"])
	}
}

// supported / configured / verified / available are separate facts: a gateway
// without the media plane never claims a live-voice target is configured or
// available, and "verified" stays false until acceptance is recorded.
func TestCapabilitiesVoiceFlagsAreIndependent(t *testing.T) {
	voiceFor := func(h http.Handler, target string) map[string]any {
		rec := doJSON(h, http.MethodGet, "/v1/capabilities", "", authHeaders(""))
		var response collectionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		for _, entry := range response.Data {
			if entry["target"] == target {
				v, _ := entry["voice"].(map[string]any)
				return v
			}
		}
		t.Fatalf("target %s missing", target)
		return nil
	}
	// Topology present, voice NOT configured on the gateway.
	_, h, _ := voiceTestServer(t, func(c *Config) { c.VoiceStore, c.VoiceMedia = nil, nil })
	v := voiceFor(h, "chatgpt_web")
	if v["supported"] != true || v["configured"] != false || v["available"] != false {
		t.Fatalf("unconfigured gateway voice flags = %v", v)
	}
	if v["verified"] != false {
		t.Fatalf("verified must stay false until acceptance is recorded: %v", v)
	}
	// Fully configured with a free account+environment.
	_, h, _ = voiceTestServer(t, func(c *Config) { c.VoiceProviderActivation = true })
	v = voiceFor(h, "chatgpt_web")
	if v["configured"] != true || v["available"] != true || v["free_resources"] != float64(1) {
		t.Fatalf("configured gateway voice flags = %v", v)
	}
	// Once the only environment is leased it is no longer available.
	if code, _ := createVoice(t, h, voiceBody("chatgpt_web")); code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	v = voiceFor(h, "chatgpt_web")
	if v["available"] != false || v["free_resources"] != float64(0) {
		t.Fatalf("leased environment still advertised free: %v", v)
	}
}

func TestCapabilitiesRequiresAuth(t *testing.T) {
	srv := NewServer(Config{AppSecret: "dev-secret", ActorRole: "service", Executor: &recordingExecutor{}})
	handler := srv.Handler()
	rec := doJSON(handler, http.MethodGet, "/v1/capabilities", "", map[string]string{})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}
}
