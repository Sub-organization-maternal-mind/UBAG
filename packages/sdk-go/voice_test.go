package ubag

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type voiceCall struct {
	Method string
	Path   string
	Query  string
	Body   string
	Auth   string
}

func voiceServer(t *testing.T, calls *[]voiceCall, handler func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, voiceCall{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Get("Authorization")})
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, WithHTTPClient(server.Client()), WithAppSecret("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestVoiceSessionLifecycleTyped(t *testing.T) {
	var calls []voiceCall
	client := voiceServer(t, &calls, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/voice/sessions":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"kind":"voice_session","session_id":"voice_1","target":"chatgpt_web","status":"queued","session":{"session_id":"voice_1","target":"chatgpt_web","mode":"live","status":"queued","muted":false,"created_at":"2026-10-05T00:00:00Z","updated_at":"2026-10-05T00:00:00Z"}}`)
		case r.URL.Path == "/v1/voice/sessions/voice_1/connect":
			_, _ = io.WriteString(w, `{"kind":"voice_session_connection","session_id":"voice_1","status":"connecting","sdp_answer":"v=0","media_credential":"cred","media_credential_expires_ms":123,"ice_servers":[{"urls":["stun:s:3478"]},{"urls":["turn:t:3478"],"username":"u","credential":"c"}]}`)
		case r.URL.Path == "/v1/voice/sessions/voice_1/mute":
			_, _ = io.WriteString(w, `{"kind":"voice_session_control","session_id":"voice_1","muted":true}`)
		case r.URL.Path == "/v1/voice/sessions/voice_1/renew":
			_, _ = io.WriteString(w, `{"kind":"voice_session_control","session_id":"voice_1","lease_expires_at":"2026-10-05T00:10:00Z"}`)
		case r.URL.Path == "/v1/voice/sessions/voice_1/terminate":
			_, _ = io.WriteString(w, `{"kind":"voice_session","session_id":"voice_1","status":"terminated","session":{"session_id":"voice_1","status":"terminated","last_error":"terminated_by_client"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/voice/sessions/voice_1":
			_, _ = io.WriteString(w, `{"kind":"voice_session","session":{"session_id":"voice_1","status":"connected","mode":"live","muted":true}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/voice/sessions":
			_, _ = io.WriteString(w, `{"api_version":"2026-05-22","kind":"voice_sessions","data":[{"session_id":"voice_1","status":"queued"}],"next_cursor":null,"trace_id":"t"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	ttl := 60

	created, err := client.CreateVoiceSession(ctx, VoiceCreateRequest{Target: "chatgpt_web", TTLSeconds: &ttl, Mode: VoiceModeLive})
	if err != nil || created.Status != VoiceStatusQueued || created.Session.SessionID != "voice_1" {
		t.Fatalf("create: %+v %v", created, err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(calls[0].Body), &sent); err != nil || sent["target"] != "chatgpt_web" || sent["ttl_seconds"] != float64(60) || sent["mode"] != "live" {
		t.Fatalf("create body: %s", calls[0].Body)
	}
	if calls[0].Auth != "Bearer s3cret" {
		t.Fatalf("auth header: %q", calls[0].Auth)
	}

	connected, err := client.ConnectVoiceSession(ctx, "voice_1", VoiceConnectRequest{SDPOffer: "offer"})
	if err != nil || connected.SDPAnswer != "v=0" || connected.MediaCredential != "cred" || connected.MediaCredentialExpiresMS != 123 {
		t.Fatalf("connect: %+v %v", connected, err)
	}
	if len(connected.ICEServers) != 2 || connected.ICEServers[1].Username != "u" || connected.ICEServers[1].URLs[0] != "turn:t:3478" {
		t.Fatalf("ice servers: %+v", connected.ICEServers)
	}
	if !strings.Contains(calls[1].Body, `"sdp_offer":"offer"`) {
		t.Fatalf("connect body: %s", calls[1].Body)
	}

	muted, err := client.MuteVoiceSession(ctx, "voice_1", true)
	if err != nil || muted.Muted == nil || !*muted.Muted || calls[2].Body != `{"muted":true}` {
		t.Fatalf("mute: %+v %v body=%s", muted, err, calls[2].Body)
	}

	renewed, err := client.RenewVoiceSessionLease(ctx, "voice_1")
	if err != nil || renewed.LeaseExpiresAt == nil || calls[3].Method != http.MethodPost || calls[3].Body != "" {
		t.Fatalf("renew: %+v %v", renewed, err)
	}

	got, err := client.GetVoiceSession(ctx, "voice_1")
	if err != nil || got.Session.Status != VoiceStatusConnected || !got.Session.Muted {
		t.Fatalf("get: %+v %v", got, err)
	}

	list, err := client.ListVoiceSessions(ctx, VoiceListParams{Limit: 5, Target: "chatgpt_web"})
	if err != nil || len(list.Data) != 1 || calls[5].Query != "limit=5&target=chatgpt_web" {
		t.Fatalf("list: %+v %v query=%s", list, err, calls[5].Query)
	}

	ended, err := client.TerminateVoiceSession(ctx, "voice_1")
	if err != nil || ended.Status != VoiceStatusTerminated || ended.Session.LastError != "terminated_by_client" {
		t.Fatalf("terminate: %+v %v", ended, err)
	}
	if calls[6].Method != http.MethodPost || calls[6].Path != "/v1/voice/sessions/voice_1/terminate" {
		t.Fatalf("terminate call: %+v", calls[6])
	}
}

func TestCreateVoiceSessionQueueFullParsesRetryAfter(t *testing.T) {
	var calls []voiceCall
	client := voiceServer(t, &calls, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"UBAG-VOICE-QUEUE-FULL-004","category":"voice","message":"queue full","retryable":true,"trace_id":"t1"}}`)
	})
	_, err := client.CreateVoiceSession(context.Background(), VoiceCreateRequest{Target: "chatgpt_web"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 APIError, got %v", err)
	}
	if apiErr.Code() != "UBAG-VOICE-QUEUE-FULL-004" || !apiErr.Retryable() {
		t.Fatalf("unexpected error details: %s retryable=%v", apiErr.Code(), apiErr.Retryable())
	}
	if ms, ok := apiErr.RetryAfterMS(); !ok || ms != 7000 {
		t.Fatalf("Retry-After header should parse to 7000ms, got %d %v", ms, ok)
	}
}

func TestRetryAfterBodyWinsOverHeader(t *testing.T) {
	var calls []voiceCall
	client := voiceServer(t, &calls, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"UBAG-VOICE-MEDIA-UNAVAILABLE-007","category":"voice","message":"x","retryable":true,"retry_after_ms":1000,"trace_id":"t"}}`)
	})
	_, err := client.ConnectVoiceSession(context.Background(), "voice_1", VoiceConnectRequest{SDPOffer: "o"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if ms, ok := apiErr.RetryAfterMS(); !ok || ms != 1000 {
		t.Fatalf("retry_after_ms should win, got %d %v", ms, ok)
	}
}

func TestListCapabilitiesTyped(t *testing.T) {
	var calls []voiceCall
	client := voiceServer(t, &calls, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"api_version":"2026-05-22","kind":"capabilities","next_cursor":null,"trace_id":"t","data":[
			{"target":"chatgpt_web","display_name":"ChatGPT","kind":"chat","safe_mode":true,"manual_login_required":true,
			 "attachments":{"max_files":4,"max_file_bytes":1024,"accepted":[{"kind":"image","content_types":["image/png"]}]},
			 "inline_message_parts":{"image_url":["image/png"],"input_audio":["audio/wav"],"remote_urls":false},
			 "voice":{"supported":true,"configured":true,"verified":false,"verified_note":"pending","available":true,"free_resources":2,"live":true,"utterance_jobs":true,"live_entry_control":"voice_button","available_accounts":2}}]}`)
	})
	caps, err := client.ListCapabilities(context.Background())
	if err != nil || calls[0].Path != "/v1/capabilities" || calls[0].Method != http.MethodGet {
		t.Fatalf("capabilities: %v %+v", err, calls)
	}
	capability, ok := caps.Find("chatgpt_web")
	if !ok || !capability.Voice.Supported || !capability.Voice.Configured || capability.Voice.Verified || !capability.Voice.Available || capability.Voice.FreeResources != 2 {
		t.Fatalf("voice capability: %+v", capability.Voice)
	}
	if capability.Voice.VerifiedNote != "pending" || capability.InlineMessageParts.ImageURL[0] != "image/png" || capability.InlineMessageParts.RemoteURLs {
		t.Fatalf("capability: %+v", capability)
	}
	if capability.Attachments.Accepted[0].Kind != "image" || capability.Attachments.MaxFiles != 4 {
		t.Fatalf("attachments: %+v", capability.Attachments)
	}
	if _, ok := caps.Find("missing"); ok {
		t.Fatal("Find must report absent targets")
	}
}

func TestChatCompletionMultimodalParts(t *testing.T) {
	var calls []voiceCall
	client := voiceServer(t, &calls, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"chatgpt_web","choices":[{"index":0,"message":{"role":"assistant","content":"a cat"},"finish_reason":"stop"}]}`)
	})
	response, err := client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model: "chatgpt_web",
		Messages: []ChatMessage{{Role: "user", Content: []JSON{
			TextPart("what is this?"),
			ImagePart("image/png", []byte("png")),
			AudioPart([]byte("wav"), "wav"),
		}}},
	})
	if err != nil || response.Text() != "a cat" {
		t.Fatalf("chat: %+v %v", response, err)
	}
	body := calls[0].Body
	for _, want := range []string{
		`"type":"text"`,
		`"url":"data:image/png;base64,cG5n"`,
		`"input_audio":{"data":"d2F2","format":"wav"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("request body missing %s: %s", want, body)
		}
	}
	if calls[0].Path != "/v1/openai/chat/completions" {
		t.Fatalf("path: %s", calls[0].Path)
	}
}
