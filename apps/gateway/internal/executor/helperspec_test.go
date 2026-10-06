package executor

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/payloadpolicy"
)

func helperTestEnvelope() DispatchEnvelope {
	return DispatchEnvelope{
		APIVersion: "v1", JobID: "job_1", TenantID: "t1", AppID: "app1", TraceID: "tr1",
		IdempotencyKey: "idem", RetryOf: "job_0",
		Client:    map[string]any{"app_id": "app1", "sdk": "x"},
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Job: DispatchJob{
			Target: "chatgpt_web", CommandType: "chat.prompt", ConversationID: "conv1",
			Input: map[string]any{
				"prompt":                 "hi",
				"attachment_local_paths": []any{"/tmp/a.png"},
				"audio_local_path":       "/tmp/a.wav",
			},
			Options: map[string]any{
				"priority": "high", "user_data_dir": "p1", "profile_dir": "p2", "profile_path": "p3",
			},
			Callbacks: map[string]any{"webhook_url": "https://example.test/cb"},
			Context:   map[string]any{"user": "someone"},
		},
		Conversation: &DispatchConversation{Key: "conv1", ThreadRef: "https://chat.example/c/1", OnMissing: "fail"},
	}
}

func TestHelperAttemptSpecGolden(t *testing.T) {
	env := helperTestEnvelope()
	spec, err := NewHelperAttemptSpec(env, "att_1", "prof-abc")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(spec)
	want := `{"api_version":"v1","job_id":"job_1","tenant_id":"t1","app_id":"app1","trace_id":"tr1",` +
		`"attempt_id":"att_1","profile_ref":"prof-abc","target":"chatgpt_web","command_type":"chat.prompt",` +
		`"conversation_id":"conv1","input":{"prompt":"hi"},"options":{"priority":"high"},` +
		`"conversation":{"key":"conv1","thread_ref":"https://chat.example/c/1","on_missing":"fail"},` +
		`"created_at":"2026-01-02T03:04:05Z"}`
	if string(got) != want {
		t.Fatalf("golden mismatch\n got: %s\nwant: %s", got, want)
	}
	// The source envelope must be untouched (projection copies).
	if _, ok := env.Job.Input["attachment_local_paths"]; !ok {
		t.Fatal("projection mutated the source envelope input")
	}
	if _, ok := env.Job.Options["user_data_dir"]; !ok {
		t.Fatal("projection mutated the source envelope options")
	}
}

func TestHelperAttemptSpecNoDenylistedKeys(t *testing.T) {
	// Every JSON key the type can emit (struct tags, recursively) must pass the
	// payload policy key check, so adding e.g. a `cookies` field fails here.
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Ptr {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			if err := payloadpolicy.Validate(map[string]any{name: "x"}); err != nil {
				t.Errorf("spec key %q violates payload policy: %v", name, err)
			}
			if f.Type != reflect.TypeOf(time.Time{}) {
				walk(f.Type)
			}
		}
	}
	walk(reflect.TypeOf(HelperAttemptSpec{}))

	spec, err := NewHelperAttemptSpec(helperTestEnvelope(), "att_1", "prof-abc")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(spec)
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if err := payloadpolicy.Validate(generic); err != nil {
		t.Fatalf("marshalled spec violates payload policy: %v", err)
	}
	for _, banned := range []string{"callbacks", "client", "context", "attachment_local_paths", "user_data_dir", "profile_dir", "profile_path", "idempotency_key"} {
		if strings.Contains(string(raw), `"`+banned+`"`) {
			t.Errorf("spec contains banned key %q: %s", banned, raw)
		}
	}
}

func TestHelperAttemptSpecRefusals(t *testing.T) {
	for _, target := range []string{"antigravity_cli", "antigravity_sdk"} {
		env := helperTestEnvelope()
		env.Job.Target = target
		if _, err := NewHelperAttemptSpec(env, "a", "p"); !errors.Is(err, ErrHelperTargetRefused) {
			t.Errorf("%s: want ErrHelperTargetRefused, got %v", target, err)
		}
	}
	for _, ref := range []string{"", "/abs/path", "../x", "a b", strings.Repeat("a", 129)} {
		if _, err := NewHelperAttemptSpec(helperTestEnvelope(), "a", ref); !errors.Is(err, ErrHelperSpecInvalid) {
			t.Errorf("profile_ref %q: want ErrHelperSpecInvalid, got %v", ref, err)
		}
	}
	if _, err := NewHelperAttemptSpec(helperTestEnvelope(), "", "p"); !errors.Is(err, ErrHelperSpecInvalid) {
		t.Errorf("missing attempt id: got %v", err)
	}
	env := helperTestEnvelope()
	env.Job.Input["password"] = "x"
	if _, err := NewHelperAttemptSpec(env, "a", "p"); !errors.Is(err, ErrHelperSpecInvalid) {
		t.Errorf("denylisted input key must fail closed, got %v", err)
	}
}

func envMap(items []string) map[string]string {
	m := map[string]string{}
	for _, item := range items {
		if k, v, ok := strings.Cut(item, "="); ok {
			m[k] = v
		}
	}
	return m
}

func TestHelperWorkerEnvIsAllowlistMinusRemoteBrowserAndNoVNC(t *testing.T) {
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", "http://browser-viewer:9223")
	t.Setenv("UBAG_NOVNC_BASE_URL", "http://127.0.0.1:7900")
	t.Setenv("UBAG_BROWSER_ENGINE", "chromium")
	t.Setenv("UBAG_PROFILE_DIR", "profiles")
	t.Setenv("UBAG_POSTGRES_DSN", "must-not-pass")

	local := envMap(minimalWorkerEnv())
	helper := envMap(helperWorkerEnv())

	// Local worker env is unchanged: it still carries both endpoints.
	if local["UBAG_REMOTE_BROWSER_ENDPOINT"] == "" || local["UBAG_NOVNC_BASE_URL"] == "" {
		t.Fatalf("local worker env lost remote-browser/noVNC: %#v", local)
	}
	want := map[string]string{}
	for k, v := range local {
		if strings.EqualFold(k, "UBAG_REMOTE_BROWSER_ENDPOINT") || strings.EqualFold(k, "UBAG_NOVNC_BASE_URL") {
			continue
		}
		want[k] = v
	}
	if !reflect.DeepEqual(helper, want) {
		t.Fatalf("helper env != local minus two keys\n helper: %#v\n want:   %#v", helper, want)
	}
	if _, ok := helper["UBAG_POSTGRES_DSN"]; ok {
		t.Fatal("DSN leaked into helper env")
	}
}

// P4.16: a helper addresses its browser profile by the opaque profile_ref
// alone. Every caller-supplied profile path and identity label is stripped from
// the spec, wherever the envelope carries it.
func TestHelperAttemptSpecStripsProfileIdentityFields(t *testing.T) {
	env := helperTestEnvelope()
	env.Job.Options["account_binding_id"] = "acct_victim"
	env.Job.Context = map[string]any{
		"account_binding_id": "acct_victim",
		"user_data_dir":      "var/profiles/victim",
		"profile_dir":        "victim",
		"profile_path":       "victim",
		"manual_session":     map[string]any{"account_binding_id": "acct_victim"},
	}
	spec, err := NewHelperAttemptSpec(env, "att_1", "pr_ref")
	if err != nil {
		t.Fatal(err)
	}
	if spec.ProfileRef != "pr_ref" {
		t.Fatalf("profile_ref = %q", spec.ProfileRef)
	}
	raw, _ := json.Marshal(spec)
	for _, leak := range []string{"account_binding_id", "acct_victim", "user_data_dir", "profile_dir", "profile_path", "var/profiles/victim", "manual_session"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("helper spec leaks %q: %s", leak, raw)
		}
	}
	if _, ok := env.Job.Options["account_binding_id"]; !ok {
		t.Fatal("projection mutated the source envelope options")
	}
}
