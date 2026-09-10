package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/attachments"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// OpenAI-compatible facade (OET provider integration). It translates OpenAI
// shapes into native jobs plus terminal waits, so an OpenAI-speaking consumer
// needs no UBAG-native client:
//
//	POST /v1/openai/chat/completions   (sync long-poll over POST /v1/jobs)
//	GET  /v1/openai/models             (model IDs the facade accepts)
//	POST /v1/openai/audio/transcriptions (multipart audio over a held job)
//	POST /v1/openai/embeddings         (deterministic hash vectors, OpenAI shape)
//
// File attachments (PDF/image/audio/video/voice) ride the native attachment
// flows: either declare ubag_attachments in the facade body and PUT each key
// to /v1/jobs/{ubag_job_id}/artifacts/{key} (key-reference), or send the whole
// call as multipart/form-data to POST /v1/jobs with the first part named
// "job" carrying this same JSON envelope plus one file part per declared key
// (one-shot). The provider web UIs (ChatGPT/Claude/Gemini/Mistral/Perplexity
// accept document+image+audio+voice+video; DeepSeek docs+images only;
// Duck.ai PDF+images only) process the files and the provider's answer comes
// back as the chat.completion text.
//
// Deliberately unsupported, rejected with OpenAI-shaped 400s: streaming,
// tools/function calling, multimodal content parts, strict response formats.
// Token usage is estimated from character counts (browser workers report DOM
// deltas, not model tokens) and documented as such in the contract.
//
// Auth rides the shared withAuth middleware; a missing/invalid credential
// therefore keeps the gateway UBAG-AUTH-MISSING-001 envelope (the one
// mixed-shape case — changing it would mean touching shared middleware).
// Every facade-domain error below is OpenAI-shaped instead.

const (
	// defaultFacadeMaxWait bounds one facade call when UBAG_FACADE_MAX_WAIT_MS
	// is unset. Browser jobs can exceed two minutes under queue pressure; 240s keeps headroom without
	// holding client connections indefinitely.
	defaultFacadeMaxWait = 240 * time.Second
	// minFacadeWaitMs is the smallest per-request ubag_wait_ms the contract
	// accepts; anything positive below it is a client error, not a clamp.
	minFacadeWaitMs = int64(1000)
	// facadeModelSeparator splits "target|setting" model IDs.
	facadeModelSeparator = "|"
	// facadeIdempotencyPrefix namespaces facade-derived native idempotency
	// keys so they can never collide with caller-supplied ones.
	facadeIdempotencyPrefix = "facade-"
	// facadeIdempotencyVersion versions the facade fingerprint scheme. Bump
	// it whenever the fingerprinted field set changes (a new field means an
	// old key no longer identifies the same logical request — without a
	// version bump the idempotency store answers CONFLICT instead of
	// creating a new job). History: v2 added model_settings,
	// response_format, and ubag_attachments to the fingerprint.
	facadeIdempotencyVersion = "v2"
)

// Facade outcome labels for ubag_facade_jobs_total.
const (
	facadeOutcomeCompleted     = "completed"
	facadeOutcomeWaitTimeout   = "wait_timeout"
	facadeOutcomeProviderError = "provider_error"
	facadeOutcomeRejected      = "rejected"
	facadeOutcomeError         = "error"
)

type openAIFacadeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
	Name    string `json:"name,omitempty"`
}

type openAIFacadeRequest struct {
	Model          string                `json:"model"`
	Messages       []openAIFacadeMessage `json:"messages"`
	Temperature    *float64              `json:"temperature,omitempty"`
	MaxTokens      *int                  `json:"max_tokens,omitempty"`
	Stream         *bool                 `json:"stream,omitempty"`
	TopP           *float64              `json:"top_p,omitempty"`
	Tools          []any                 `json:"tools,omitempty"`
	ToolChoice     any                   `json:"tool_choice,omitempty"`
	ResponseFormat any                   `json:"response_format,omitempty"`
	UbagWaitMs     *int64                `json:"ubag_wait_ms,omitempty"`
	// UbagStrict opts back into fail-closed picker config: when true, a
	// drifted model menu fails the job as selector_drift_detected (the old
	// default). When omitted or false (recommended), the facade marks the
	// job best-effort and the worker skips the picker instead — the prompt
	// submits in the account's current mode rather than failing.
	UbagStrict *bool `json:"ubag_strict,omitempty"`
	// UbagAttachments carries native attachment declarations
	// ({key, content_type, kind, [filename]}) for this facade call. Each
	// declared key MUST be uploaded with PUT
	// /v1/jobs/{ubag_job_id}/artifacts/{key} before the facade wait budget
	// expires; the facade waits on the held job and dispatches it once every
	// declared key is present. Rejected when the resolved target's manifest
	// attachments policy does not accept the declared kind/content_type.
	UbagAttachments []any `json:"ubag_attachments,omitempty"`
}

// facadeJSONMode classifies the response_format subset the facade can serve
// honestly. "off" means no format requested. "coerce" means the caller asked
// for a JSON object or schema: the facade does NOT change what the provider
// writes, it only post-processes the completion text (fence-strip, then
// substring object/array extraction) and fails the call with
// json_extract_failed when nothing parses, so a downstream JSON parser never
// receives provider chatter. Anything else (e.g. json_schema strict modes the
// gateway cannot enforce) stays a 400.
type facadeJSONMode int

const (
	facadeJSONOff facadeJSONMode = iota
	facadeJSONCoerce
)

func classifyFacadeResponseFormat(raw any) (facadeJSONMode, string, bool) {
	if raw == nil {
		return facadeJSONOff, "", true
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return facadeJSONOff, "", false
	}
	formatType, _ := obj["type"].(string)
	switch strings.ToLower(strings.TrimSpace(formatType)) {
	case "json_object":
		return facadeJSONCoerce, "", true
	case "json_schema":
		schema, _ := obj["json_schema"].(map[string]any)
		name, _ := schema["name"].(string)
		return facadeJSONCoerce, strings.TrimSpace(name), true
	default:
		return facadeJSONOff, "", false
	}
}

// coerceFacadeJSON extracts the first parseable JSON object or array from
// provider text: trim, strip one ``` fenced block, then scan for the first
// balanced {...} or [...] span that parses. Returns the compacted JSON and
// the top-level kind ("object"/"array"). Providers often wrap answers in
// prose ("Here is the JSON: ..."); without this the caller would have to
// trust provider discipline it cannot observe.
func coerceFacadeJSON(text string) (string, string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", "", false
	}
	if strings.HasPrefix(trimmed, "```") {
		if firstNewline := strings.Index(trimmed, "\n"); firstNewline >= 0 {
			trimmed = trimmed[firstNewline+1:]
		}
		if end := strings.LastIndex(trimmed, "```"); end >= 0 {
			trimmed = trimmed[:end]
		}
		trimmed = strings.TrimSpace(trimmed)
	}
	if trimmed == "" {
		return "", "", false
	}
	if json.Valid([]byte(trimmed)) {
		var probe any
		if err := json.Unmarshal([]byte(trimmed), &probe); err == nil {
			switch probe.(type) {
			case map[string]any:
				return string(mustCompactFacadeJSON(trimmed)), "object", true
			case []any:
				return string(mustCompactFacadeJSON(trimmed)), "object", true
			}
		}
	}
	for i, r := range trimmed {
		if r != '{' && r != '[' {
			continue
		}
		for end := len(trimmed); end > i; end-- {
			candidate := strings.TrimSpace(trimmed[i:end])
			if candidate == "" {
				continue
			}
			if !json.Valid([]byte(candidate)) {
				continue
			}
			var probe any
			if err := json.Unmarshal([]byte(candidate), &probe); err != nil {
				continue
			}
			switch probe.(type) {
			case map[string]any:
				return string(mustCompactFacadeJSON(candidate)), "object", true
			case []any:
				return string(mustCompactFacadeJSON(candidate)), "object", true
			}
		}
	}
	return "", "", false
}

func mustCompactFacadeJSON(raw string) []byte {
	var probe any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return []byte(strings.TrimSpace(raw))
	}
	compacted, err := json.Marshal(probe)
	if err != nil {
		return []byte(strings.TrimSpace(raw))
	}
	return compacted
}

type openAIFacadeError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
	Param   string `json:"param,omitempty"`
}

type openAIFacadeErrorEnvelope struct {
	Error openAIFacadeError `json:"error"`
}

type openAIFacadeChoiceMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIFacadeChoice struct {
	Index        int                       `json:"index"`
	Message      openAIFacadeChoiceMessage `json:"message"`
	FinishReason string                    `json:"finish_reason"`
}

type openAIFacadeUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIFacadeCompletion struct {
	ID        string               `json:"id"`
	Object    string               `json:"object"`
	Created   int64                `json:"created"`
	Model     string               `json:"model"`
	Choices   []openAIFacadeChoice `json:"choices"`
	Usage     openAIFacadeUsage    `json:"usage"`
	UbagJobID string               `json:"ubag_job_id"`
}

// openAIFacadeAccepted is the 202 answer for a facade call whose native job is
// held for attachment uploads. It is NOT a chat.completion: no choices/usage
// exist yet. The caller uploads each declared key with
// PUT /v1/jobs/{ubag_job_id}/artifacts/{key}, then polls
// GET /v1/jobs/{ubag_job_id} (or replays the same facade body to wait).
type openAIFacadeAccepted struct {
	Object    string `json:"object"`
	Model     string `json:"model"`
	UbagJobID string `json:"ubag_job_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

type openAIFacadeModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type openAIFacadeModelList struct {
	Object string              `json:"object"`
	Data   []openAIFacadeModel `json:"data"`
}

func (s *Server) writeFacadeError(w http.ResponseWriter, status int, errType, code, message string) {
	s.writeJSON(w, status, openAIFacadeErrorEnvelope{
		Error: openAIFacadeError{Message: message, Type: errType, Code: code},
	})
}

func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	s.writeJSON(w, http.StatusOK, openAIFacadeModelList{Object: "list", Data: s.facadeModels()})
}

// facadeModels lists one model per target plus one target|setting entry per
// caller-selectable model-catalog choice, mirroring the manifests the native
// create path validates against. Deterministic order: catalog order, settings
// sorted by key, values in manifest order.
func (s *Server) facadeModels() []openAIFacadeModel {
	models := []openAIFacadeModel{}
	for _, entry := range targetCatalog() {
		key, _ := entry["key"].(string)
		if key == "" {
			continue
		}
		models = append(models, openAIFacadeModel{ID: key, Object: "model", OwnedBy: "ubag"})
		for _, id := range facadeChoiceModelIDs(key) {
			models = append(models, openAIFacadeModel{ID: id, Object: "model", OwnedBy: "ubag"})
		}
		// Board-curated composite IDs (not manifest values — see
		// resolveFacadeModel). Listed here so Discover models surfaces the
		// operator's recommended pick.
		for _, id := range facadeCuratedModelIDs(key) {
			models = append(models, openAIFacadeModel{ID: id, Object: "model", OwnedBy: "ubag"})
		}
	}
	return models
}

// facadeChoiceModelIDs lists the chat-addressable target|value IDs for one
// target: one per value of every kind=choice catalog setting, in sorted
// setting-key order with manifest value order preserved. Toggle-kind
// settings are skipped — they carry no labelled values and cannot be chat
// model IDs. Shared with resolveFacadeModel so the models list and the
// resolver can never disagree on what is addressable.
func facadeChoiceModelIDs(target string) []string {
	catalog := resolveModelCatalog(target)
	settingKeys := make([]string, 0, len(catalog.Settings))
	for name := range catalog.Settings {
		settingKeys = append(settingKeys, name)
	}
	sort.Strings(settingKeys)
	ids := []string{}
	for _, name := range settingKeys {
		setting := catalog.Settings[name]
		if setting.Kind != "choice" {
			continue
		}
		for _, value := range setting.Values {
			ids = append(ids, target+facadeModelSeparator+value)
		}
	}
	return ids
}

// facadeCuratedModelIDs lists board-curated composite IDs for one target:
// operator-recommended setting combinations that are not single manifest
// values (see resolveFacadeModel). Shared with facadeModels so the list and
// the resolver agree.
func facadeCuratedModelIDs(target string) []string {
	if target == "chatgpt_web" {
		return []string{"chatgpt_web" + facadeModelSeparator + "GPT-5.6 Sol + Medium"}
	}
	return nil
}

func (s *Server) handleOpenAIChatCompletion(w http.ResponseWriter, r *http.Request) {
	outcome := facadeOutcomeError
	defer func() { s.facadeOutcomes.add(outcome) }()

	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		outcome = facadeOutcomeRejected
		return
	}

	// Plain decode (NOT decodeBody): OpenAI clients send fields the facade
	// ignores (logit_bias, seed, ...), and DisallowUnknownFields would 400
	// them. Unknown fields are ignored; the rejected subset is checked below.
	limited := http.MaxBytesReader(w, r.Body, s.maxBody)
	raw, err := io.ReadAll(limited)
	if err != nil {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "request_too_large", "request body exceeds gateway limit")
		outcome = facadeOutcomeRejected
		return
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "request body must be valid JSON")
		outcome = facadeOutcomeRejected
		return
	}
	var req openAIFacadeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "request body must be valid JSON")
		outcome = facadeOutcomeRejected
		return
	}

	if req.Stream != nil && *req.Stream {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "streaming_unsupported", "streaming is not supported; retry with stream:false")
		outcome = facadeOutcomeRejected
		return
	}
	if len(req.Tools) > 0 || req.ToolChoice != nil {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "tools_unsupported", "tools are not supported by the UBAG facade")
		outcome = facadeOutcomeRejected
		return
	}
	// response_format: only the coercible JSON subset is served (post-process
	// extraction with a hard failure when nothing parses). The gateway cannot
	// enforce provider-side schema compliance, so strict/unknown shapes stay a
	// 400 rather than a silent lie.
	jsonMode, _, ok := classifyFacadeResponseFormat(req.ResponseFormat)
	if !ok {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "structured_output_unsupported", "response_format is not supported; only {\"type\":\"json_object\"} and {\"type\":\"json_schema\"} coercion are served")
		outcome = facadeOutcomeRejected
		return
	}

	target, modelSettings, ok := s.resolveFacadeModel(req.Model)
	if !ok {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "model_not_found", fmt.Sprintf("model %q is not available", req.Model))
		outcome = facadeOutcomeRejected
		return
	}
	attachmentDecls, ok := normalizeFacadeAttachments(req.UbagAttachments)
	if !ok {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "ubag_attachments must be an array of {key, content_type, kind} objects")
		outcome = facadeOutcomeRejected
		return
	}
	prompt, ok := flattenFacadeMessages(req.Messages)
	if !ok {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "messages must contain at least one text message with a system, user, or assistant role")
		outcome = facadeOutcomeRejected
		return
	}
	// JSON-coercion hint: provider web UIs answer in prose unless the task
	// explicitly demands JSON. When the OET caller asked for a JSON shape,
	// say so in the provider prompt as well — the gateway-side extractor
	// still enforces the guarantee on the way out. The hint is folded into
	// the idempotency fingerprint (not the raw prompt) so identical caller
	// bodies always derive identical native keys.
	jsonHint := ""
	if jsonMode == facadeJSONCoerce {
		jsonHint = "json"
	}

	wait := s.facadeMaxWait
	if req.UbagWaitMs != nil {
		ms := *req.UbagWaitMs
		if ms < minFacadeWaitMs {
			s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "ubag_wait_ms must be at least 1000")
			outcome = facadeOutcomeRejected
			return
		}
		wait = time.Duration(ms) * time.Millisecond
		if wait > s.facadeMaxWait {
			wait = s.facadeMaxWait
		}
	}

	jobID, status, errType, code, message, ok := s.createFacadeJob(r, req, target, modelSettings, prompt, attachmentDecls, jsonHint)
	if !ok {
		s.writeFacadeError(w, status, errType, code, message)
		if status == http.StatusBadRequest {
			outcome = facadeOutcomeRejected
		}
		return
	}

	// Held attachment jobs (status created) need their artifact PUTs before the
	// native wait can resolve. The uploads arrive on separate connections, so
	// the in-process WaitEvents loop would hold this HTTP connection until the
	// facade deadline; answer 202 immediately instead so the caller can upload
	// the keys and then poll the job (or replay this same body to wait on it).
	// The job ID rides in the payload for machine use.
	if len(attachmentDecls) > 0 {
		if held, found, err := s.jobs.Get(r.Context(), jobID); err == nil && found && held.Status == jobstore.StatusCreated {
			s.writeJSON(w, http.StatusAccepted, openAIFacadeAccepted{
				Object:    "chat.completion.chunk",
				Model:     req.Model,
				UbagJobID: jobID,
				Status:    string(jobstore.StatusCreated),
				Message:   fmt.Sprintf("job held for attachment uploads; PUT each declared key to /v1/jobs/%s/artifacts/{key}, then poll GET /v1/jobs/%s or replay this request to wait", jobID, jobID),
			})
			outcome = facadeOutcomeCompleted
			return
		}
	}

	job, result := s.waitFacadeJob(r, jobID, wait)
	switch result {
	case facadeWaitDone:
		completion, status, errType, code, message, param := s.facadeCompletion(req.Model, prompt, job, jsonMode)
		if status != http.StatusOK {
			if param != "" {
				s.writeJSON(w, status, openAIFacadeErrorEnvelope{
					Error: openAIFacadeError{Message: message, Type: errType, Code: code, Param: param},
				})
			} else {
				s.writeFacadeError(w, status, errType, code, message)
			}
			outcome = facadeOutcomeProviderError
			return
		}
		s.writeJSON(w, http.StatusOK, completion)
		outcome = facadeOutcomeCompleted
	case facadeWaitTimeout:
		// The still-running job ID rides in param for machine use; the
		// message keeps the human-readable poll hint.
		s.writeJSON(w, http.StatusGatewayTimeout, openAIFacadeErrorEnvelope{
			Error: openAIFacadeError{
				Message: fmt.Sprintf("job did not finish within the facade deadline; poll GET /v1/jobs/%s for the result", jobID),
				Type:    "timeout_error",
				Code:    "wait_timeout",
				Param:   jobID,
			},
		})
		outcome = facadeOutcomeWaitTimeout
	case facadeWaitAbort:
		// Client disconnected; the job was cancelled best-effort. Nothing
		// left to write to.
		outcome = facadeOutcomeError
	default:
		s.writeFacadeError(w, http.StatusInternalServerError, "server_error", "job_wait_failed", "failed while waiting for the job result")
		outcome = facadeOutcomeError
	}
}

// resolveFacadeModel maps a facade model ID to a native target plus validated
// model settings. A bare target uses operator defaults; "target|value" binds
// the catalog choice setting offering that value (e.g. chatgpt_web|GPT-5.6
// Sol binds {"model": ...}, deepseek_web|Instant binds {"mode": ...}).
// Thinking levels ride the same mechanism: chatgpt_web|Medium binds
// {"thinking": "Medium"}, duckai_web|Reasoning binds {"reasoning":
// "Reasoning"}. Toggle-kind settings (gemini_web thinking, deepseek
// deepthink) have no labelled values, so they are NOT addressable as model
// IDs — the facade surfaces them in the models list for discovery but
// rejects them as chat model IDs; use the bare target (operator default)
// for those.
//
// Composite ChatGPT ID: the OET board offers ONE curated entry,
// "chatgpt_web|GPT-5.6 Sol + Medium", which binds BOTH settings at once
// ({"model": "GPT-5.6 Sol", "thinking": "Medium"}) — the operator's
// always-Sol-Medium default as a single pick, so a stale or wrong-effort
// combo can never be selected.
func (s *Server) resolveFacadeModel(model string) (string, map[string]any, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", nil, false
	}
	target, value := model, ""
	if i := strings.Index(model, facadeModelSeparator); i >= 0 {
		target, value = strings.TrimSpace(model[:i]), strings.TrimSpace(model[i+1:])
	}
	if !isTargetKey(target) || !facadeTargetKnown(target) {
		return "", nil, false
	}
	if value == "" {
		return target, nil, true
	}
	// Composite ChatGPT ID (board-curated, not a manifest value): bind both
	// the model and the thinking level at once.
	if target == "chatgpt_web" && value == "GPT-5.6 Sol + Medium" {
		return target, map[string]any{"model": "GPT-5.6 Sol", "thinking": "Medium"}, true
	}
	catalog := resolveModelCatalog(target)
	for name, setting := range catalog.Settings {
		if setting.Kind == "choice" && slices.Contains(setting.Values, value) {
			return target, map[string]any{name: value}, true
		}
	}
	return "", nil, false
}

func facadeTargetKnown(target string) bool {
	for _, entry := range targetCatalog() {
		if key, _ := entry["key"].(string); key == target {
			return true
		}
	}
	return false
}

// normalizeFacadeAttachments converts the untyped ubag_attachments field into
// the native attachment declaration list ([]any of {key, content_type, kind,
// [filename]} maps). Shape errors fail closed here; per-target manifest policy
// is enforced later by the shared validateAttachmentsForCreate path. Returns
// nil (not an error) when the caller declares nothing.
func normalizeFacadeAttachments(raw []any) ([]any, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	out := make([]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		for property := range entry {
			switch property {
			case "key", "content_type", "kind", "filename":
			default:
				return nil, false
			}
		}
		key, _ := entry["key"].(string)
		contentType, _ := entry["content_type"].(string)
		kind, _ := entry["kind"].(string)
		if strings.TrimSpace(key) == "" || strings.TrimSpace(contentType) == "" || strings.TrimSpace(kind) == "" {
			return nil, false
		}
		out = append(out, map[string]any{
			"key":          strings.TrimSpace(key),
			"content_type": strings.TrimSpace(contentType),
			"kind":         strings.TrimSpace(kind),
		})
		if filename, _ := entry["filename"].(string); strings.TrimSpace(filename) != "" {
			out[len(out)-1].(map[string]any)["filename"] = strings.TrimSpace(filename)
		}
	}
	return out, true
}

// flattenFacadeMessages collapses OpenAI message history into one prompt:
// system messages first (in order), then user/assistant turns in order.
// Non-string content (multimodal parts) and unknown roles fail the request.
func flattenFacadeMessages(messages []openAIFacadeMessage) (string, bool) {
	if len(messages) == 0 {
		return "", false
	}
	var systems []string
	var turns []string
	for _, msg := range messages {
		text, ok := msg.Content.(string)
		if !ok {
			return "", false
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		switch msg.Role {
		case "system":
			systems = append(systems, text)
		case "user":
			turns = append(turns, "user: "+text)
		case "assistant":
			turns = append(turns, "assistant: "+text)
		default:
			return "", false
		}
	}
	var parts []string
	if len(systems) > 0 {
		parts = append(parts, strings.Join(systems, "\n"))
	}
	parts = append(parts, turns...)
	prompt := strings.TrimSpace(strings.Join(parts, "\n"))
	if prompt == "" {
		return "", false
	}
	return prompt, true
}

// createFacadeJob runs the shared native create path (validation, authz,
// kill switch, payload safety, model settings, attachments, plugins,
// idempotency, enqueue) by capturing its HTTP response, then returns the
// created job ID. The native idempotency key is derived from the caller
// principal plus the request hash (now including attachment declarations),
// so retrying an identical facade body replays the same job instead of
// submitting the provider twice.
//
// Attachment semantics match the native key-reference flow: a facade call
// that declares ubag_attachments creates a HELD job (status created). The
// facade answers 202 immediately with the job ID; the caller uploads each
// declared key with PUT /v1/jobs/{ubag_job_id}/artifacts/{key}. The job
// dispatches once every declared key is present; polling
// GET /v1/jobs/{ubag_job_id} (or replaying the same facade body) then
// resolves it into a chat.completion as usual.
func (s *Server) createFacadeJob(r *http.Request, req openAIFacadeRequest, target string, modelSettings map[string]any, prompt string, attachmentDecls []any, jsonHint string) (jobID string, status int, errType, code, message string, ok bool) {
	tenantID, appID := requestScope(r)
	// Fingerprint every field that changes what the provider does: model,
	// messages, sampling hints, format coercion (+ the derived JSON hint —
	// same body must still derive the same key, so the hint is a pure
	// function of the fingerprinted response_format), attachments, strict
	// flag, AND the resolved model settings. The resolved settings matter:
	// two calls with the same "chatgpt_web" string but different picker
	// states (or before/after a manifest change) are different provider
	// requests. A missing field here is a future IDEMPOTENCY-CONFLICT-001
	// every time that field is introduced — hence the version below.
	keySeed, err := json.Marshal(map[string]any{
		"fingerprint_version": facadeIdempotencyVersion,
		"model":               req.Model,
		"model_settings":      modelSettings,
		"messages":            facadeMessageDigest(req.Messages),
		"temperature":         req.Temperature,
		"max_tokens":          req.MaxTokens,
		"top_p":               req.TopP,
		"response_format":     req.ResponseFormat,
		"ubag_strict":         req.UbagStrict,
		"ubag_attachments":    attachmentDecls,
	})
	if err != nil {
		return "", http.StatusInternalServerError, "server_error", "job_create_failed", "failed to fingerprint the request", false
	}
	idempotencyKey := facadeIdempotencyPrefix + hashBytes(append([]byte(tenantID+"\n"+appID+"\n"), keySeed...))

	// Provider-visible prompt: the JSON hint is part of the task (when the
	// caller asked for a JSON shape), so it must reach the provider — but it
	// is derived from the fingerprinted response_format, never from free
	// caller text, so identical bodies still derive identical keys.
	effectivePrompt := prompt
	if jsonHint != "" {
		effectivePrompt = "Return your answer as a single JSON object or array only, with no surrounding prose or markdown fences.\n\n" + prompt
	}

	options := map[string]any{"return_mode": "final"}
	if req.Temperature != nil {
		options["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		options["max_tokens"] = *req.MaxTokens
	}
	if req.TopP != nil {
		options["top_p"] = *req.TopP
	}
	// Best-effort picker config: the provider's model menu drifts (ChatGPT
	// rewrote the picker 2026-08-10; Gemini flattened its menu 2026-07-17).
	// The worker resolves operator defaults from its selectors; this flag
	// only RECORDS that the caller asked for best-effort mode. A drifted
	// picker then skips (submits in the account's current mode) instead of
	// failing the job as selector_drift_detected. Pass ubag_strict:true to
	// keep the old fail-closed behavior for evals.
	//
	// The marker rides inside the facade's model-settings map (not raw
	// options) so optionsWithProviderConfig merges it with the validated
	// pins below: marker first, pins on top (skip-if-drifted AND
	// pin-if-present). Setting options directly here would be stripped as
	// client provider_config and drop the pins (Sol/Medium, 3.8 Flash,
	// Instant) — the worker would run unconfigured.
	if req.UbagStrict == nil || !*req.UbagStrict {
		if modelSettings == nil {
			modelSettings = map[string]any{}
		}
		if _, ok := modelSettings["_enabled"]; !ok {
			modelSettings["_enabled"] = false
		}
	}
	input := map[string]any{"prompt": effectivePrompt}
	if len(attachmentDecls) > 0 {
		input["attachments"] = attachmentDecls
	}
	createReq := createJobRequest{
		APIVersion:     s.apiVersion,
		IdempotencyKey: idempotencyKey,
		Client: clientRequest{
			AppID:      appID,
			AppVersion: "1.0.0",
			SDK:        sdkRequest{Name: "ubag-openai-facade", Version: s.version},
		},
		Job: jobRequest{
			Target:        target,
			CommandType:   "chat.prompt",
			Input:         input,
			ModelSettings: modelSettings,
			Options:       options,
			Context:       map[string]any{"correlation_id": idempotencyKey},
		},
	}

	// prepareCreateJob writes UBAG-shaped errors; capture them for mapping.
	prec := httptest.NewRecorder()
	prepared, ok := s.prepareCreateJob(prec, r, createReq)
	if !ok {
		recStatus, recType, recCode, recMsg, _ := mapRecorderError(prec)
		return "", recStatus, recType, recCode, recMsg, false
	}
	ctx := context.WithValue(r.Context(), preparedCreateJobKey{}, prepared)
	crec := httptest.NewRecorder()
	s.createJob(crec, r.WithContext(ctx))
	if crec.Code != http.StatusAccepted {
		recStatus, recType, recCode, recMsg, _ := mapRecorderError(crec)
		return "", recStatus, recType, recCode, recMsg, false
	}
	var created jobResponse
	if err := json.Unmarshal(crec.Body.Bytes(), &created); err != nil || created.JobID == "" {
		return "", http.StatusInternalServerError, "server_error", "job_create_failed", "job creation returned an unreadable response", false
	}
	return created.JobID, 0, "", "", "", true
}

// facadeMessageDigest reduces messages to role/content pairs for the replay
// hash. Content is guaranteed textual by request validation upstream.
func facadeMessageDigest(messages []openAIFacadeMessage) []map[string]string {
	out := make([]map[string]string, 0, len(messages))
	for _, msg := range messages {
		text, _ := msg.Content.(string)
		out = append(out, map[string]string{"role": msg.Role, "content": text})
	}
	return out
}

// mapRecorderError translates a captured native-path error response into an
// OpenAI-shaped status triple, preserving the native message (which carries
// the UBAG code) for debuggability.
func mapRecorderError(rec *httptest.ResponseRecorder) (status int, errType, code, message string, ok bool) {
	var env errorEnvelope
	nativeMsg := "request failed"
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err == nil && env.Error.Code != "" {
		nativeMsg = fmt.Sprintf("%s (%s)", env.Error.Message, env.Error.Code)
	}
	switch {
	case rec.Code == http.StatusTooManyRequests:
		return 429, "rate_limit_error", "rate_limited", nativeMsg, false
	case rec.Code == http.StatusServiceUnavailable:
		return 503, "provider_error", "provider_unavailable", nativeMsg, false
	case rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden:
		return 401, "authentication_error", "invalid_api_key", nativeMsg, false
	case rec.Code >= 500:
		return 500, "server_error", "job_create_failed", nativeMsg, false
	default:
		return 400, "invalid_request_error", "job_rejected", nativeMsg, false
	}
}

type facadeWaitResult int

const (
	// facadeWaitDone: the job reached a terminal state.
	facadeWaitDone facadeWaitResult = iota
	// facadeWaitTimeout: the budget expired; the job may still be running.
	facadeWaitTimeout
	// facadeWaitAbort: the client disconnected; the job was cancelled
	// best-effort and no response may be written.
	facadeWaitAbort
	// facadeWaitError: the store failed; answer 500.
	facadeWaitError
)

// waitFacadeJob blocks until the job is terminal using store WaitEvents (no
// polling spin: the call sleeps until an event lands or the budget expires).
// It is only reached for jobs that already dispatched: attachment-declaring
// calls whose native job is still held (status created, awaiting artifact
// PUTs) are answered 202 up front instead, so this loop never has to wait on
// uploads arriving over other connections.
func (s *Server) waitFacadeJob(r *http.Request, jobID string, wait time.Duration) (jobstore.Job, facadeWaitResult) {
	deadline := time.Now().Add(wait)
	lastSeq := 0
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return jobstore.Job{}, facadeWaitTimeout
		}
		waitCtx, cancel := context.WithTimeout(r.Context(), remaining)
		events, found, err := s.jobs.WaitEvents(waitCtx, jobID, lastSeq, 1)
		cancel()
		if err != nil {
			// A store-level error is NOT a client disconnect: surfacing
			// facadeWaitError answers 500 instead of cancelling a live job
			// (a cancelled facade job shows up downstream as
			// "job ended as cancelled" with the provider blamed).
			// Only treat an error as a client abort when the request
			// context itself is done AND the inner wait was not the
			// deadline that fired.
			if waitCtx.Err() == context.DeadlineExceeded && r.Context().Err() == nil {
				return jobstore.Job{}, facadeWaitTimeout
			}
			if r.Context().Err() != nil {
				if job, ok, _ := s.jobs.Get(context.Background(), jobID); ok {
					s.cancelFacadeJob(context.Background(), job, "facade_client_disconnect")
				}
				return jobstore.Job{}, facadeWaitAbort
			}
			return jobstore.Job{}, facadeWaitError
		}
		if !found {
			return jobstore.Job{}, facadeWaitError
		}
		for _, ev := range events {
			if ev.Sequence > lastSeq {
				lastSeq = ev.Sequence
			}
		}
		job, ok, err := s.jobs.Get(r.Context(), jobID)
		if err != nil || !ok {
			return jobstore.Job{}, facadeWaitError
		}
		if jobstore.TerminalStatus(job.Status) {
			return job, facadeWaitDone
		}
	}
}

// cancelFacadeJob best-effort cancels a facade-owned job (client disconnect).
// It mirrors cancelJob's store/dispatcher steps minus the client mutation
// reservation (there is no caller key to reserve) and minus webhook enqueue
// (facade jobs carry no callbacks).
func (s *Server) cancelFacadeJob(ctx context.Context, job jobstore.Job, reason string) {
	if jobstore.TerminalStatus(job.Status) {
		return
	}
	_ = s.executor.CancelJob(ctx, job, reason)
	updated, found, err := s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusCanceled)
	if err != nil || !found {
		return
	}
	if jobstore.TerminalStatus(updated.Status) {
		s.releaseConcurrencyTokenForJob(job.ID)
	}
}

// facadeCompletion renders a terminal job as an OpenAI chat.completion.
// Terminal failures map to retryable 503s (login drift, transient) or 500s.
// When jsonMode is coerce, the provider text is reduced to its first parseable
// JSON value first; a completion with no parseable JSON fails as 500
// json_extract_failed (with the job ID in param for native inspection)
// instead of returning provider chatter to a JSON parser.
func (s *Server) facadeCompletion(model, prompt string, job jobstore.Job, jsonMode facadeJSONMode) (openAIFacadeCompletion, int, string, string, string, string) {
	switch job.Status {
	case jobstore.StatusCompleted, jobstore.StatusCompletedWithWarnings:
		text := facadeOutputText(buildJobResultEnvelope(job))
		if text == "" {
			return openAIFacadeCompletion{}, http.StatusInternalServerError, "server_error", "empty_completion", "job completed without extractable text", ""
		}
		if jsonMode == facadeJSONCoerce {
			coerced, _, ok := coerceFacadeJSON(text)
			if !ok {
				return openAIFacadeCompletion{}, http.StatusInternalServerError, "server_error", "json_extract_failed", "job " + job.ID + " completed but no JSON object or array could be extracted from the provider output", job.ID
			}
			text = coerced
		}
		promptTokens := estimateTokens(len(prompt))
		completionTokens := estimateTokens(len(text))
		created := job.UpdatedAt.Unix()
		if created == 0 {
			created = time.Now().Unix()
		}
		return openAIFacadeCompletion{
			ID:      "cmpl-" + job.ID,
			Object:  "chat.completion",
			Created: created,
			Model:   model,
			Choices: []openAIFacadeChoice{{
				Index:        0,
				Message:      openAIFacadeChoiceMessage{Role: "assistant", Content: text},
				FinishReason: "stop",
			}},
			Usage: openAIFacadeUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			},
			UbagJobID: job.ID,
		}, http.StatusOK, "", "", "", ""
	default:
		signals := s.deriveJobSignals(context.Background(), job)
		detail := signals.ErrorMessage
		if detail == "" {
			detail = fmt.Sprintf("job ended as %s", string(job.Status))
		}
		if signals.ErrorClass == loginRequiredErrorClass {
			return openAIFacadeCompletion{}, http.StatusServiceUnavailable, "provider_error", "provider_login_required", detail, ""
		}
		switch job.Status {
		case jobstore.StatusFailedRetryable:
			return openAIFacadeCompletion{}, http.StatusServiceUnavailable, "provider_error", "provider_transient", detail, ""
		case jobstore.StatusTimedOut:
			return openAIFacadeCompletion{}, http.StatusInternalServerError, "server_error", "provider_timeout", detail, ""
		case jobstore.StatusCanceled:
			return openAIFacadeCompletion{}, http.StatusInternalServerError, "server_error", "job_cancelled", detail, ""
		default:
			return openAIFacadeCompletion{}, http.StatusInternalServerError, "server_error", "provider_failed", detail, ""
		}
	}
}

func facadeOutputText(env *JobResultEnvelope) string {
	if env == nil || env.Output == nil {
		return ""
	}
	if env.Output.Text != "" {
		return env.Output.Text
	}
	return env.Output.PlainText
}

// estimateTokens converts characters to tokens at ~4 chars/token. Browser
// workers report DOM deltas, not model tokens, so every facade usage figure
// is an estimate — the contract says so, and callers must not treat these as
// metered model tokens.
func estimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

// ─────────────────────────────────────────────────────────────────────────────
// OpenAI audio transcriptions: multipart audio over a held native job.
// ─────────────────────────────────────────────────────────────────────────────

// transcriptionAudioMIMEs is the fail-closed allowlist for the audio file
// part. It mirrors the audio/voice content types the live provider manifests
// actually accept (chatgpt/claude/gemini/mistral/perplexity), minus container
// variants the worker has no evidence for.
var transcriptionAudioMIMEs = map[string]string{
	"audio/webm":  "webm",
	"audio/wav":   "wav",
	"audio/x-wav": "wav",
	"audio/mpeg":  "mp3",
	"audio/mp4":   "m4a",
	"audio/ogg":   "ogg",
}

const (
	// maxTranscriptionAudioBytes caps the uploaded audio part. 24 MiB matches
	// the largest OET caller cap (class recordings) and stays under the 32
	// MiB per-file artifact ceiling.
	maxTranscriptionAudioBytes = 24 << 20
	// defaultTranscriptionTarget is the provider used when the caller passes
	// model "whisper-1" (the OET convention) or omits a UBAG target.
	defaultTranscriptionTarget = "chatgpt_web"
)

type openAITranscriptionResponse struct {
	Text      string `json:"text"`
	UbagJobID string `json:"ubag_job_id"`
}

// handleOpenAITranscription implements POST /v1/openai/audio/transcriptions:
// an OpenAI-shaped multipart upload (file + optional model/language/prompt)
// bridged onto one native job with the audio attached, resolved into plain
// transcript text. The provider web UI does the listening; the gateway only
// moves bytes and returns what the provider wrote.
//
// Flow: validate part -> create held job (ubag_attachments) -> PUT artifact
// bytes server-side (no second client round-trip) -> wait terminal ->
// completion text. Anything that cannot transcribe fails with an
// OpenAI-shaped error; native codes ride in the message for debuggability.
func (s *Server) handleOpenAITranscription(w http.ResponseWriter, r *http.Request) {
	outcome := facadeOutcomeError
	defer func() { s.facadeOutcomes.add(outcome) }()

	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		outcome = facadeOutcomeRejected
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || strings.TrimSpace(params["boundary"]) == "" {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "content-type must be multipart/form-data with a boundary")
		outcome = facadeOutcomeRejected
		return
	}

	reader := multipart.NewReader(io.LimitReader(r.Body, int64(maxTranscriptionAudioBytes)+s.maxBody), params["boundary"])
	var audioBytes []byte
	var audioMIME, audioFilename, modelField, language, promptHint string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "multipart body is malformed")
			outcome = facadeOutcomeRejected
			return
		}
		name := part.FormName()
		switch name {
		case "file":
			if audioBytes != nil {
				s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "multipart part \"file\" appears more than once")
				outcome = facadeOutcomeRejected
				return
			}
			audioMIME = safeArtifactContentType(part.Header.Get("Content-Type"))
			audioFilename = part.FileName()
			chunk, err := io.ReadAll(io.LimitReader(part, int64(maxTranscriptionAudioBytes)+1))
			_ = part.Close()
			if err != nil {
				s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "audio part could not be read")
				outcome = facadeOutcomeRejected
				return
			}
			audioBytes = chunk
		case "model":
			raw, _ := io.ReadAll(io.LimitReader(part, 256))
			_ = part.Close()
			modelField = strings.TrimSpace(string(raw))
		case "language":
			raw, _ := io.ReadAll(io.LimitReader(part, 16))
			_ = part.Close()
			language = strings.ToLower(strings.TrimSpace(string(raw)))
		case "prompt":
			raw, _ := io.ReadAll(io.LimitReader(part, s.maxBody))
			_ = part.Close()
			promptHint = strings.TrimSpace(string(raw))
		default:
			_ = part.Close()
		}
	}
	if len(audioBytes) == 0 {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "multipart part \"file\" with audio bytes is required")
		outcome = facadeOutcomeRejected
		return
	}
	if len(audioBytes) > maxTranscriptionAudioBytes {
		s.writeFacadeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "file_too_large", "audio part exceeds the 24 MiB transcription limit")
		outcome = facadeOutcomeRejected
		return
	}
	ext, ok := transcriptionAudioMIMEs[strings.ToLower(strings.TrimSpace(audioMIME))]
	if !ok {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "unsupported_audio_type", fmt.Sprintf("audio content type %q is not accepted for transcription", audioMIME))
		outcome = facadeOutcomeRejected
		return
	}

	target := transcriptionTargetForModel(modelField)

	prompt := "Transcribe the attached audio verbatim. Return plain text only, no commentary."
	if promptHint != "" {
		prompt = promptHint + "\n\nTranscribe the attached audio verbatim. Return plain text only, no commentary."
	}
	if language != "" {
		prompt = fmt.Sprintf("The audio language is %s. %s", language, prompt)
	}

	key := "audio." + ext
	if base := strings.TrimSpace(audioFilename); base != "" && attachments.ValidKey(base) {
		key = base
	}
	attachmentDecls := []any{map[string]any{
		"key":          key,
		"content_type": strings.ToLower(strings.TrimSpace(audioMIME)),
		"kind":         "voice",
	}}

	facadeReq := openAIFacadeRequest{Model: target}
	jobID, status, errType, code, message, ok := s.createFacadeJob(r, facadeReq, target, nil, prompt, attachmentDecls, "")
	if !ok {
		s.writeFacadeError(w, status, errType, code, message)
		if status == http.StatusBadRequest {
			outcome = facadeOutcomeRejected
		}
		return
	}
	if err := s.putFacadeAttachmentBytes(r.Context(), jobID, key, strings.ToLower(strings.TrimSpace(audioMIME)), audioBytes); err != nil {
		s.writeFacadeError(w, http.StatusInternalServerError, "server_error", "attachment_store_failed", "transcription audio could not be stored")
		outcome = facadeOutcomeError
		return
	}

	job, result := s.waitFacadeJob(r, jobID, s.facadeMaxWait)
	switch result {
	case facadeWaitDone:
		if job.Status != jobstore.StatusCompleted && job.Status != jobstore.StatusCompletedWithWarnings {
			_, status, errType, code, message, _ := s.facadeCompletion(target, prompt, job, facadeJSONOff)
			s.writeFacadeError(w, status, errType, code, message)
			outcome = facadeOutcomeProviderError
			return
		}
		text := facadeOutputText(buildJobResultEnvelope(job))
		if strings.TrimSpace(text) == "" {
			s.writeFacadeError(w, http.StatusInternalServerError, "server_error", "empty_transcript", "job completed without extractable transcript text")
			outcome = facadeOutcomeProviderError
			return
		}
		s.writeJSON(w, http.StatusOK, openAITranscriptionResponse{Text: text, UbagJobID: jobID})
		outcome = facadeOutcomeCompleted
	case facadeWaitTimeout:
		s.writeJSON(w, http.StatusGatewayTimeout, openAIFacadeErrorEnvelope{
			Error: openAIFacadeError{
				Message: fmt.Sprintf("transcription did not finish within the facade deadline; poll GET /v1/jobs/%s for the result", jobID),
				Type:    "timeout_error",
				Code:    "wait_timeout",
				Param:   jobID,
			},
		})
		outcome = facadeOutcomeWaitTimeout
	case facadeWaitAbort:
		outcome = facadeOutcomeError
	default:
		s.writeFacadeError(w, http.StatusInternalServerError, "server_error", "job_wait_failed", "failed while waiting for the transcription result")
		outcome = facadeOutcomeError
	}
}

// transcriptionTargetForModel maps the caller's model field onto a UBAG live
// target. "whisper-1" (and blanks/unknowns) mean the operator default — the
// gateway never pretends to run Whisper weights; the provider web UI listens
// instead. A bare UBAG target passes through when callers want to pin one.
func transcriptionTargetForModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" || strings.EqualFold(trimmed, "whisper-1") {
		return defaultTranscriptionTarget
	}
	if isTargetKey(trimmed) && facadeTargetKnown(trimmed) {
		return trimmed
	}
	return defaultTranscriptionTarget
}

// putFacadeAttachmentBytes stores one attachment's bytes directly into the
// artifact store for a facade-owned held job, then runs the shared dispatch
// gate so the job leaves StatusCreated once complete. It mirrors the PUT
// artifact path's store + dispatch steps without an HTTP round-trip.
func (s *Server) putFacadeAttachmentBytes(ctx context.Context, jobID, key, contentType string, payload []byte) error {
	job, found, err := s.jobs.Get(ctx, jobID)
	if err != nil || !found {
		return fmt.Errorf("load held job: %w", err)
	}
	if _, err := s.artifactSt.PutArtifact(ctx, job.ID, key, contentType, bytes.NewReader(payload), int64(len(payload))); err != nil {
		return fmt.Errorf("store artifact: %w", err)
	}
	s.artifactCaptures.Add(1)
	s.attachmentsStored.Add(1)
	s.attachmentOutcomes.add("voice|stored")
	return s.maybeDispatchAfterArtifact(ctx, job)
}

// ─────────────────────────────────────────────────────────────────────────────
// OpenAI embeddings: deterministic hash vectors in the OpenAI shape.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// defaultEmbeddingDim matches the OET callers (text-embedding-3-small,
	// 1536) so existing rows and vector columns keep working unchanged.
	defaultEmbeddingDim = 1536
	// maxEmbeddingInputsPerCall bounds one embeddings call the way the OET
	// EmbeddingService batches (20).
	maxEmbeddingInputsPerCall = 20
)

type openAIEmbeddingRequest struct {
	Model      string `json:"model"`
	Input      any    `json:"input"`
	Dimensions *int   `json:"dimensions,omitempty"`
}

type openAIEmbeddingDatum struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type openAIEmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type openAIEmbeddingResponse struct {
	Object string                 `json:"object"`
	Data   []openAIEmbeddingDatum `json:"data"`
	Model  string                 `json:"model"`
	Usage  openAIEmbeddingUsage   `json:"usage"`
}

// handleOpenAIEmbeddings implements POST /v1/openai/embeddings in the exact
// OpenAI response shape ({object:list, data[{object:embedding, index,
// embedding[float]}], model, usage}). The vectors are deterministic
// SHA-256-chained unit vectors, NOT semantic embeddings: no model runs, so
// cosine neighbours mean "same bytes", never "same meaning". The contract
// says so explicitly, and callers that need semantic retrieval must keep a
// real embedding provider. What this buys OET: every embeddings.generate /
// writing.exemplar.embed.v1 call resolves to a correctly-shaped,
// correctly-dimensioned, correctly-indexed vector with zero new
// infrastructure and zero per-call spend.
func (s *Server) handleOpenAIEmbeddings(w http.ResponseWriter, r *http.Request) {
	outcome := facadeOutcomeError
	defer func() { s.facadeOutcomes.add(outcome) }()

	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, r, http.MethodPost)
		outcome = facadeOutcomeRejected
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}

	limited := http.MaxBytesReader(w, r.Body, s.maxBody)
	raw, err := io.ReadAll(limited)
	if err != nil {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "request_too_large", "request body exceeds gateway limit")
		outcome = facadeOutcomeRejected
		return
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "request body must be valid JSON")
		outcome = facadeOutcomeRejected
		return
	}
	var req openAIEmbeddingRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "request body must be valid JSON")
		outcome = facadeOutcomeRejected
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "model is required")
		outcome = facadeOutcomeRejected
		return
	}
	inputs := facadeEmbeddingInputs(req.Input)
	if len(inputs) == 0 {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "input must be a non-empty string or array of strings")
		outcome = facadeOutcomeRejected
		return
	}
	if len(inputs) > maxEmbeddingInputsPerCall {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", fmt.Sprintf("input carries %d texts; at most %d per call", len(inputs), maxEmbeddingInputsPerCall))
		outcome = facadeOutcomeRejected
		return
	}
	dim := defaultEmbeddingDim
	if req.Dimensions != nil {
		if *req.Dimensions <= 0 || *req.Dimensions > defaultEmbeddingDim {
			s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", fmt.Sprintf("dimensions must be 1..%d", defaultEmbeddingDim))
			outcome = facadeOutcomeRejected
			return
		}
		dim = *req.Dimensions
	}

	data := make([]openAIEmbeddingDatum, 0, len(inputs))
	promptTokens := 0
	for i, text := range inputs {
		promptTokens += estimateTokens(len(text))
		data = append(data, openAIEmbeddingDatum{
			Object:    "embedding",
			Index:     i,
			Embedding: facadeHashEmbedding(text, dim),
		})
	}
	s.writeJSON(w, http.StatusOK, openAIEmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  strings.TrimSpace(req.Model),
		Usage:  openAIEmbeddingUsage{PromptTokens: promptTokens, TotalTokens: promptTokens},
	})
	outcome = facadeOutcomeCompleted
}

func facadeEmbeddingInputs(raw any) []string {
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil
			}
			out = append(out, text)
		}
		return out
	default:
		return nil
	}
}

// facadeHashEmbedding is a deterministic SHA-256-chained unit vector over the
// lowercased, trimmed text. Same bytes -> same vector, always. It is a
// stable stand-in for dedupe and shape-compatibility, never a semantic
// embedding: neighbours share bytes, not meaning.
func facadeHashEmbedding(text string, dim int) []float64 {
	vec := make([]float64, dim)
	seed := []byte(strings.ToLower(strings.TrimSpace(text)))
	if len(seed) == 0 {
		return vec
	}
	offset := 0
	iteration := 0
	for offset < dim {
		probe := make([]byte, 0, len(seed)+4)
		probe = append(probe, seed...)
		probe = append(probe, byte(iteration), byte(iteration>>8), byte(iteration>>16), byte(iteration>>24))
		sum := sha256.Sum256(probe)
		for i := 0; i+4 <= len(sum) && offset < dim; i += 4 {
			bits := uint32(sum[i]) | uint32(sum[i+1])<<8 | uint32(sum[i+2])<<16 | uint32(sum[i+3])<<24
			vec[offset] = float64(int32(bits)) / 2147483648.0
			offset++
		}
		iteration++
	}
	var magnitude float64
	for _, v := range vec {
		magnitude += v * v
	}
	magnitude = math.Sqrt(magnitude)
	if magnitude > 0 {
		for i := range vec {
			vec[i] /= magnitude
		}
	}
	return vec
}
