package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

func facadeTestServer() *Server {
	return NewServer(Config{
		AppSecret:     "dev-secret",
		ActorRole:     "service",
		Executor:      &recordingExecutor{},
		FacadeMaxWait: 60 * time.Second,
	})
}

func TestDefaultFacadeWaitCoversQueueAndBrowserExecution(t *testing.T) {
	if defaultFacadeMaxWait != 240*time.Second {
		t.Fatalf("default facade wait = %s, want 4m", defaultFacadeMaxWait)
	}
	server := NewServer(Config{})
	if server.facadeMaxWait != 240*time.Second {
		t.Fatalf("server facade wait = %s, want 4m", server.facadeMaxWait)
	}
}

func decodeFacadeError(t *testing.T, recBody []byte) openAIFacadeErrorEnvelope {
	t.Helper()
	var env openAIFacadeErrorEnvelope
	if err := json.Unmarshal(recBody, &env); err != nil {
		t.Fatalf("decode facade error: %v; body=%s", err, recBody)
	}
	return env
}

func TestFacadeModelsListsTargets(t *testing.T) {
	server := facadeTestServer().Handler()
	rec := doJSON(server, http.MethodGet, "/v1/openai/models", "", authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("models status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var list openAIFacadeModelList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if list.Object != "list" || len(list.Data) == 0 {
		t.Fatalf("models list = %+v, want non-empty list", list)
	}
	ids := map[string]bool{}
	for _, m := range list.Data {
		ids[m.ID] = true
		if m.Object != "model" || m.OwnedBy != "ubag" {
			t.Fatalf("model entry = %+v, want object=model owned_by=ubag", m)
		}
	}
	for _, want := range []string{"mock", "chatgpt_web", "deepseek_web", "gemini_web"} {
		if !ids[want] {
			t.Fatalf("models missing %q: %v", want, ids)
		}
	}
}

func TestFacadeRejectsStreamingAndTools(t *testing.T) {
	server := facadeTestServer().Handler()
	cases := []struct {
		name string
		body string
		code string
	}{
		{"stream", `{"model":"mock","messages":[{"role":"user","content":"hi"}],"stream":true}`, "streaming_unsupported"},
		{"tools", `{"model":"mock","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`, "tools_unsupported"},
		{"tool_choice", `{"model":"mock","messages":[{"role":"user","content":"hi"}],"tool_choice":"auto"}`, "tools_unsupported"},
		{"response_format_text", `{"model":"mock","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"text"}}`, "structured_output_unsupported"},
		{"unknown_model", `{"model":"bogus|nope","messages":[{"role":"user","content":"hi"}]}`, "model_not_found"},
		{"empty_messages", `{"model":"mock","messages":[]}`, "invalid_request"},
		{"bad_role", `{"model":"mock","messages":[{"role":"tool","content":"hi"}]}`, "invalid_request"},
		{"short_wait", `{"model":"mock","messages":[{"role":"user","content":"hi"}],"ubag_wait_ms":50}`, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(server, http.MethodPost, "/v1/openai/chat/completions", tc.body, authHeaders(""))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			env := decodeFacadeError(t, rec.Body.Bytes())
			if env.Error.Code != tc.code {
				t.Fatalf("error code = %q, want %q; body=%s", env.Error.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestResolveFacadeModel(t *testing.T) {
	server := facadeTestServer()
	target, settings, ok := server.resolveFacadeModel("chatgpt_web")
	if !ok || target != "chatgpt_web" || len(settings) != 0 {
		t.Fatalf("bare target = %q,%v,%v", target, settings, ok)
	}
	target, settings, ok = server.resolveFacadeModel("chatgpt_web|GPT-5.6 Sol")
	if !ok || target != "chatgpt_web" || settings["model"] != "GPT-5.6 Sol" {
		t.Fatalf("target|setting = %q,%v,%v", target, settings, ok)
	}
	target, settings, ok = server.resolveFacadeModel("deepseek_web|Instant")
	if !ok || target != "deepseek_web" || settings["mode"] != "Instant" {
		t.Fatalf("deepseek setting = %q,%v,%v", target, settings, ok)
	}
	// Thinking levels are choice-kind settings, so they resolve as model IDs
	// through the same path (chatgpt thinking, duckai reasoning).
	target, settings, ok = server.resolveFacadeModel("chatgpt_web|Medium")
	if !ok || target != "chatgpt_web" || settings["thinking"] != "Medium" {
		t.Fatalf("thinking setting = %q,%v,%v", target, settings, ok)
	}
	// Board-curated composite: Sol + Medium bound at once.
	target, settings, ok = server.resolveFacadeModel("chatgpt_web|GPT-5.6 Sol + Medium")
	if !ok || target != "chatgpt_web" || settings["model"] != "GPT-5.6 Sol" || settings["thinking"] != "Medium" {
		t.Fatalf("composite setting = %q,%v,%v", target, settings, ok)
	}
	target, settings, ok = server.resolveFacadeModel("duckai_web|Reasoning")
	if !ok || target != "duckai_web" || settings["reasoning"] != "Reasoning" {
		t.Fatalf("reasoning setting = %q,%v,%v", target, settings, ok)
	}
	for _, bad := range []string{"", "bogus_target", "chatgpt_web|No Such Model", "mock|anything"} {
		if _, _, ok := server.resolveFacadeModel(bad); ok {
			t.Fatalf("model %q should not resolve", bad)
		}
	}
}

func TestFacadeChoiceModelIDsMatchResolver(t *testing.T) {
	// The models list and the resolver must agree: every listed target|value
	// resolves, and every resolving target|value is listed. Otherwise the
	// admin board offers IDs the facade rejects (the reported mismatch).
	for _, entry := range targetCatalog() {
		key, _ := entry["key"].(string)
		if key == "" {
			continue
		}
		for _, id := range facadeChoiceModelIDs(key) {
			if _, _, ok := facadeTestServer().resolveFacadeModel(id); !ok {
				t.Fatalf("listed model %q does not resolve", id)
			}
		}
		for _, id := range facadeCuratedModelIDs(key) {
			if _, _, ok := facadeTestServer().resolveFacadeModel(id); !ok {
				t.Fatalf("listed curated model %q does not resolve", id)
			}
		}
	}
}

func TestFlattenFacadeMessages(t *testing.T) {
	prompt, ok := flattenFacadeMessages([]openAIFacadeMessage{
		{Role: "user", Content: "first"},
		{Role: "system", Content: "be brief"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "second"},
	})
	if !ok {
		t.Fatal("flatten should succeed")
	}
	if !strings.HasPrefix(prompt, "be brief") {
		t.Fatalf("system must lead the prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "user: first") || !strings.Contains(prompt, "assistant: ok") || !strings.Contains(prompt, "user: second") {
		t.Fatalf("turns out of order in %q", prompt)
	}
	if _, ok := flattenFacadeMessages(nil); ok {
		t.Fatal("empty history must fail")
	}
	if _, ok := flattenFacadeMessages([]openAIFacadeMessage{{Role: "user", Content: []any{"multimodal"}}}); ok {
		t.Fatal("non-string content must fail")
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens(0); got != 0 {
		t.Fatalf("estimate(0) = %d, want 0", got)
	}
	if got := estimateTokens(8); got != 2 {
		t.Fatalf("estimate(8) = %d, want 2", got)
	}
}

func TestFacadeAttachmentsHeldJobResolves(t *testing.T) {
	srv := NewServer(Config{
		AppSecret:     "dev-secret",
		ActorRole:     "service",
		Executor:      &recordingExecutor{},
		FacadeMaxWait: 10 * time.Second,
	})
	handler := srv.Handler()
	body := `{"model":"chatgpt_web","messages":[{"role":"user","content":"transcribe the attached audio"}],"ubag_attachments":[{"key":"note.webm","content_type":"audio/webm","kind":"voice"}]}`

	rec := doJSON(handler, http.MethodPost, "/v1/openai/chat/completions", body, authHeaders(""))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("attachment declare status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var accepted openAIFacadeAccepted
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode 202: %v", err)
	}
	if accepted.UbagJobID == "" || accepted.Status != string(jobstore.StatusCreated) {
		t.Fatalf("accepted = %+v, want ubag_job_id + held status", accepted)
	}

	listed, err := srv.jobs.List(t.Context(), jobstore.ListFilter{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("backing jobs = %v, err = %v", listed, err)
	}
	held := listed[0]
	if held.Status != jobstore.StatusCreated {
		t.Fatalf("backing job status = %q, want held %q", held.Status, jobstore.StatusCreated)
	}

	put := doRaw(handler, http.MethodPut, "/v1/jobs/"+held.ID+"/artifacts/note.webm", "fake-opus", "audio/webm", authHeaders("idem_facade_attach_put"))
	if put.Code != http.StatusCreated {
		t.Fatalf("artifact put status = %d, want 201; body=%s", put.Code, put.Body.String())
	}

	traceID := held.TraceID
	if traceID == "" {
		traceID = "trace_facade_attach_test"
	}
	if _, _, err := srv.jobs.ApplyWorkerEvent(t.Context(), jobstore.WorkerEvent{
		EventID:    "evt_facade_attach_completion",
		JobID:      held.ID,
		APIVersion: held.APIVersion,
		Type:       "completed",
		TraceID:    traceID,
		Data: map[string]any{
			"status": "completed",
			"result": map[string]any{"type": "text", "text": "transcript: hello"},
		},
	}); err != nil {
		t.Fatalf("apply completion event: %v", err)
	}

	// Same body replays the same finished job and resolves the completion.
	replay := doJSON(handler, http.MethodPost, "/v1/openai/chat/completions", body, authHeaders(""))
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status = %d; body=%s", replay.Code, replay.Body.String())
	}
	var completion openAIFacadeCompletion
	if err := json.Unmarshal(replay.Body.Bytes(), &completion); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	if completion.Choices[0].Message.Content != "transcript: hello" {
		t.Fatalf("choice content = %q", completion.Choices[0].Message.Content)
	}
	if completion.UbagJobID != held.ID {
		t.Fatalf("job link = %q, want %q", completion.UbagJobID, held.ID)
	}
}

func TestFacadeAttachmentsValidation(t *testing.T) {
	server := facadeTestServer().Handler()
	cases := []struct {
		name string
		body string
	}{
		{"missing kind", `{"model":"chatgpt_web","messages":[{"role":"user","content":"hi"}],"ubag_attachments":[{"key":"a.pdf","content_type":"application/pdf"}]}`},
		{"unknown property", `{"model":"chatgpt_web","messages":[{"role":"user","content":"hi"}],"ubag_attachments":[{"key":"a.pdf","content_type":"application/pdf","kind":"document","size":3}]}`},
		{"unsupported target", `{"model":"mock","messages":[{"role":"user","content":"hi"}],"ubag_attachments":[{"key":"a.pdf","content_type":"application/pdf","kind":"document"}]}`},
		{"content type rejected", `{"model":"chatgpt_web","messages":[{"role":"user","content":"hi"}],"ubag_attachments":[{"key":"x.exe","content_type":"application/x-msdownload","kind":"document"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(server, http.MethodPost, "/v1/openai/chat/completions", tc.body, authHeaders(""))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestClassifyFacadeResponseFormat(t *testing.T) {
	if mode, _, ok := classifyFacadeResponseFormat(nil); !ok || mode != facadeJSONOff {
		t.Fatalf("nil = %v,%v, want off,true", mode, ok)
	}
	if mode, _, ok := classifyFacadeResponseFormat(map[string]any{"type": "json_object"}); !ok || mode != facadeJSONCoerce {
		t.Fatalf("json_object = %v,%v, want coerce,true", mode, ok)
	}
	if mode, name, ok := classifyFacadeResponseFormat(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "part_a_manifest",
			"schema": map[string]any{"type": "object"},
		},
	}); !ok || mode != facadeJSONCoerce || name != "part_a_manifest" {
		t.Fatalf("json_schema = %v,%q,%v, want coerce,part_a_manifest,true", mode, name, ok)
	}
	if _, _, ok := classifyFacadeResponseFormat(map[string]any{"type": "text"}); ok {
		t.Fatal("text must stay rejected")
	}
	if _, _, ok := classifyFacadeResponseFormat("json_object"); ok {
		t.Fatal("non-object format must stay rejected")
	}
}

func TestCoerceFacadeJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain object", `{"a":1}`, `{"a":1}`},
		{"fenced", "```json\n{\"a\": 1}\n```", `{"a":1}`},
		{"prose wrapped", `Here is the JSON: {"a":1} thanks`, `{"a":1}`},
		{"array", `[1,2]`, `[1,2]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, ok := coerceFacadeJSON(tc.in)
			if !ok || got != tc.want {
				t.Fatalf("coerce(%q) = %q,%v, want %q,true", tc.in, got, ok, tc.want)
			}
		})
	}
	for _, bad := range []string{"", "no json here", "```\n```"} {
		if _, _, ok := coerceFacadeJSON(bad); ok {
			t.Fatalf("coerce(%q) must fail", bad)
		}
	}
}

func TestFacadeJSONCoerceCompletion(t *testing.T) {
	srv := NewServer(Config{
		AppSecret:     "dev-secret",
		ActorRole:     "service",
		Executor:      &recordingExecutor{},
		FacadeMaxWait: 10 * time.Second,
	})
	handler := srv.Handler()
	body := `{"model":"mock","messages":[{"role":"user","content":"UBAG_FACADE_JSON_PROBE"}],"response_format":{"type":"json_object"}}`

	complete := func(job jobstore.Job) {
		traceID := job.TraceID
		if traceID == "" {
			traceID = "trace_facade_json_test"
		}
		_, _, err := srv.jobs.ApplyWorkerEvent(t.Context(), jobstore.WorkerEvent{
			EventID:    "evt_facade_json_completion",
			JobID:      job.ID,
			APIVersion: job.APIVersion,
			Type:       "completed",
			TraceID:    traceID,
			Data: map[string]any{
				"status": "completed",
				"result": map[string]any{"type": "text", "text": "Sure — {\"summary\":\"ok\"} — done."},
			},
		})
		if err != nil {
			t.Errorf("apply completion event: %v", err)
		}
	}
	waitForJob := func() jobstore.Job {
		for i := 0; i < 2000; i++ {
			listed, err := srv.jobs.List(t.Context(), jobstore.ListFilter{})
			if err != nil {
				t.Fatalf("list jobs: %v", err)
			}
			if len(listed) > 0 {
				return listed[0]
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("facade never created the backing job")
		return jobstore.Job{}
	}

	done := make(chan struct {
		code int
		body []byte
	})
	go func() {
		rec := doJSON(handler, http.MethodPost, "/v1/openai/chat/completions", body, authHeaders(""))
		done <- struct {
			code int
			body []byte
		}{rec.Code, rec.Body.Bytes()}
	}()
	complete(waitForJob())
	first := <-done
	if first.code != http.StatusOK {
		t.Fatalf("json coerce status = %d; body=%s", first.code, first.body)
	}
	var completion openAIFacadeCompletion
	if err := json.Unmarshal(first.body, &completion); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	if got := completion.Choices[0].Message.Content; got != `{"summary":"ok"}` {
		t.Fatalf("coerced content = %q", got)
	}
}

func TestTranscriptionTargetForModel(t *testing.T) {
	for model, want := range map[string]string{
		"":            defaultTranscriptionTarget,
		"whisper-1":   defaultTranscriptionTarget,
		"chatgpt_web": "chatgpt_web",
		"bogus":       defaultTranscriptionTarget,
	} {
		if got := transcriptionTargetForModel(model); got != want {
			t.Fatalf("target(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestFacadeHashEmbedding(t *testing.T) {
	vec := facadeHashEmbedding("hello world", 1536)
	if len(vec) != 1536 {
		t.Fatalf("len = %d, want 1536", len(vec))
	}
	var magnitude float64
	for _, v := range vec {
		magnitude += v * v
	}
	if magnitude < 0.99 || magnitude > 1.01 {
		t.Fatalf("magnitude = %v, want unit vector", magnitude)
	}
	again := facadeHashEmbedding("hello world", 1536)
	for i := range vec {
		if vec[i] != again[i] {
			t.Fatal("hash embedding must be deterministic")
		}
	}
	other := facadeHashEmbedding("different text", 1536)
	same := true
	for i := range vec {
		if vec[i] != other[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("distinct texts must not share a vector")
	}
	if got := len(facadeHashEmbedding("hello", 8)); got != 8 {
		t.Fatalf("dim override len = %d, want 8", got)
	}
}

func TestOpenAIEmbeddingsHandler(t *testing.T) {
	server := facadeTestServer().Handler()
	body := `{"model":"text-embedding-3-small","input":["hello","world"]}`
	rec := doJSON(server, http.MethodPost, "/v1/openai/embeddings", body, authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp openAIEmbeddingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode embeddings: %v", err)
	}
	if resp.Object != "list" || len(resp.Data) != 2 {
		t.Fatalf("response = %+v", resp)
	}
	for i, datum := range resp.Data {
		if datum.Object != "embedding" || datum.Index != i || len(datum.Embedding) != 1536 {
			t.Fatalf("datum %d = %+v", i, datum)
		}
	}
	if resp.Usage.TotalTokens <= 0 {
		t.Fatalf("usage = %+v", resp.Usage)
	}

	for _, bad := range []string{
		`{"model":"","input":["hi"]}`,
		`{"model":"m","input":[]}`,
		`{"model":"m","input":[""]}`,
		`{"model":"m","input":""}`,
	} {
		rec := doJSON(server, http.MethodPost, "/v1/openai/embeddings", bad, authHeaders(""))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bad body %s status = %d, want 400", bad, rec.Code)
		}
	}
}

// A facade call over a non-completing executor hits the wait budget and
// answers 504 with the still-running job ID in error.param. The job must
// NOT be cancelled by the timeout: a later worker completion still resolves
// the native job (cancel is reserved for a real client disconnect).
func TestFacadeWaitTimeoutReturnsJobID(t *testing.T) {
	srv := NewServer(Config{
		AppSecret:     "dev-secret",
		ActorRole:     "service",
		Executor:      &recordingExecutor{},
		FacadeMaxWait: 80 * time.Millisecond,
	})
	server := srv.Handler()
	body := `{"model":"mock","messages":[{"role":"user","content":"UBAG_FACADE_TIMEOUT_PROBE"}],"ubag_wait_ms":60000}`
	start := time.Now()
	rec := doJSON(server, http.MethodPost, "/v1/openai/chat/completions", body, authHeaders(""))
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("facade call took %v, budget was 80ms", elapsed)
	}
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504; body=%s", rec.Code, rec.Body.String())
	}
	env := decodeFacadeError(t, rec.Body.Bytes())
	if env.Error.Code != "wait_timeout" {
		t.Fatalf("error code = %q, want wait_timeout", env.Error.Code)
	}
	if !strings.HasPrefix(env.Error.Param, "job_") {
		t.Fatalf("error.param = %q, want the native job ID", env.Error.Param)
	}
	listed, err := srv.jobs.List(t.Context(), jobstore.ListFilter{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("backing jobs = %v, err = %v", listed, err)
	}
	if jobstore.TerminalStatus(listed[0].Status) {
		t.Fatalf("timed-out facade wait cancelled the backing job: status=%q", listed[0].Status)
	}
}

// A worker completion event arriving mid-wait resolves the facade call with
// an OpenAI completion; an identical retry replays the same native job.
func TestFacadeCompletionAndIdempotentReplay(t *testing.T) {
	srv := NewServer(Config{
		AppSecret:     "dev-secret",
		ActorRole:     "service",
		Executor:      &recordingExecutor{},
		FacadeMaxWait: 10 * time.Second,
	})
	handler := srv.Handler()
	body := `{"model":"mock","messages":[{"role":"user","content":"UBAG_FACADE_REPLAY_PROBE"}]}`

	complete := func(job jobstore.Job) {
		traceID := job.TraceID
		if traceID == "" {
			traceID = "trace_facade_test"
		}
		_, _, err := srv.jobs.ApplyWorkerEvent(t.Context(), jobstore.WorkerEvent{
			EventID:    "evt_facade_test_completion",
			JobID:      job.ID,
			APIVersion: job.APIVersion,
			Type:       "completed",
			TraceID:    traceID,
			Data: map[string]any{
				"status": "completed",
				"result": map[string]any{"type": "text", "text": "UBAG_FACADE_MOCK_OK"},
			},
		})
		if err != nil {
			t.Errorf("apply completion event: %v", err)
		}
	}
	waitForJob := func() jobstore.Job {
		for i := 0; i < 2000; i++ {
			listed, err := srv.jobs.List(t.Context(), jobstore.ListFilter{})
			if err != nil {
				t.Fatalf("list jobs: %v", err)
			}
			if len(listed) > 0 {
				return listed[0]
			}
			time.Sleep(5 * time.Millisecond)
		}

		t.Fatal("facade never created the backing job")
		return jobstore.Job{}
	}

	done := make(chan struct {
		code int
		body []byte
	})
	go func() {
		rec := doJSON(handler, http.MethodPost, "/v1/openai/chat/completions", body, authHeaders(""))
		done <- struct {
			code int
			body []byte
		}{rec.Code, rec.Body.Bytes()}
	}()
	firstJob := waitForJob()
	firstJobID := firstJob.ID
	complete(firstJob)
	first := <-done
	if first.code != http.StatusOK {
		t.Fatalf("first call status = %d; body=%s", first.code, first.body)
	}
	var completion openAIFacadeCompletion
	if err := json.Unmarshal(first.body, &completion); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	if completion.Object != "chat.completion" || len(completion.Choices) != 1 {
		t.Fatalf("completion shape = %+v", completion)
	}
	got := completion.Choices[0]
	if got.Message.Role != "assistant" || got.Message.Content != "UBAG_FACADE_MOCK_OK" || got.FinishReason != "stop" {
		t.Fatalf("choice = %+v", got)
	}
	if completion.UbagJobID != firstJobID || completion.ID != "cmpl-"+firstJobID {
		t.Fatalf("job link = %q/%q, want %q", completion.ID, completion.UbagJobID, firstJobID)
	}
	if completion.Usage.TotalTokens != completion.Usage.PromptTokens+completion.Usage.CompletionTokens || completion.Usage.TotalTokens <= 0 {
		t.Fatalf("usage = %+v", completion.Usage)
	}

	// Identical body: the derived idempotency key replays the same finished
	// job, so the second call resolves immediately with the same job ID.
	rec := doJSON(handler, http.MethodPost, "/v1/openai/chat/completions", body, authHeaders(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var replayed openAIFacadeCompletion
	if err := json.Unmarshal(rec.Body.Bytes(), &replayed); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if replayed.UbagJobID != firstJobID {
		t.Fatalf("replay job = %q, want %q", replayed.UbagJobID, firstJobID)
	}
}

func TestFacadeFingerprintCoversNonce(t *testing.T) {
	srv := NewServer(Config{
		AppSecret:     "dev-secret",
		ActorRole:     "service",
		Executor:      &recordingExecutor{},
		FacadeMaxWait: 80 * time.Millisecond,
	})
	handler := srv.Handler()

	jobID := func(nonce string) string {
		body := fmt.Sprintf(`{"model":"mock","messages":[{"role":"user","content":"ping"}],"ubag_nonce":%q}`, nonce)
		rec := doJSON(handler, http.MethodPost, "/v1/openai/chat/completions", body, authHeaders(""))
		if rec.Code != http.StatusGatewayTimeout {
			t.Fatalf("status = %d, want 504; body=%s", rec.Code, rec.Body.String())
		}
		return decodeFacadeError(t, rec.Body.Bytes()).Error.Param
	}

	firstID := jobID("first")
	if replayID := jobID("first"); replayID != firstID {
		t.Fatalf("same nonce created job %q, want replay of %q", replayID, firstID)
	}
	secondID := jobID("second")
	if secondID == firstID {
		t.Fatalf("different nonces replayed job %q", firstID)
	}
}

func TestFacadeFingerprintWithoutNonceRemainsCompatible(t *testing.T) {
	srv := NewServer(Config{
		AppSecret:     "dev-secret",
		ActorRole:     "service",
		Executor:      &recordingExecutor{},
		FacadeMaxWait: 80 * time.Millisecond,
	})
	rec := doJSON(srv.Handler(), http.MethodPost, "/v1/openai/chat/completions",
		`{"model":"mock","messages":[{"role":"user","content":"ping"}]}`, authHeaders(""))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504; body=%s", rec.Code, rec.Body.String())
	}
	jobID := decodeFacadeError(t, rec.Body.Bytes()).Error.Param
	job, found, err := srv.jobs.Get(t.Context(), jobID)
	if err != nil || !found {
		t.Fatalf("get job: %v", err)
	}
	const legacyKey = "facade-1177fdc00eced51222c3d53d0f96e96181ae53f1991241ec3a436f2d46f593a9"
	if got := job.Context["correlation_id"]; got != legacyKey {
		t.Fatalf("no-nonce key = %q, want legacy key %q", got, legacyKey)
	}
}

// Same model string with different resolved model settings must NOT replay
// the same job: the settings change what the provider does (e.g. a manifest
// change between deploys, or different picker states). Regression test for
// the admin-board UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001 on retest — that
// error meant the key did NOT cover the settings; now the key covers them,
// so the second call creates a new job instead of conflicting.
func TestFacadeFingerprintCoversModelSettings(t *testing.T) {
	srv := NewServer(Config{
		AppSecret:     "dev-secret",
		ActorRole:     "service",
		Executor:      &recordingExecutor{},
		FacadeMaxWait: 80 * time.Millisecond,
	})
	handler := srv.Handler()
	body := `{"model":"mock","messages":[{"role":"user","content":"UBAG_FACADE_SETTINGS_PROBE"}]}`

	first := doJSON(handler, http.MethodPost, "/v1/openai/chat/completions", body, authHeaders(""))
	if first.Code != http.StatusGatewayTimeout {
		t.Fatalf("first status = %d, want 504; body=%s", first.Code, first.Body.String())
	}
	firstID := decodeFacadeError(t, first.Body.Bytes()).Error.Param
	if !strings.HasPrefix(firstID, "job_") {
		t.Fatalf("first param = %q, want job ID", firstID)
	}

	// Same body again replays (same settings → same key → same job).
	replay := doJSON(handler, http.MethodPost, "/v1/openai/chat/completions", body, authHeaders(""))
	if replay.Code != http.StatusGatewayTimeout {
		t.Fatalf("replay status = %d, want 504; body=%s", replay.Code, replay.Body.String())
	}
	if got := decodeFacadeError(t, replay.Body.Bytes()).Error.Param; got != firstID {
		t.Fatalf("replay param = %q, want %q", got, firstID)
	}
}
