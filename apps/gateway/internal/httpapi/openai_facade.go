package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// OpenAI-compatible facade (OET provider integration). It translates a narrow
// OpenAI chat-completions subset into one native job plus a terminal wait, so
// an OpenAI-speaking consumer needs no UBAG-native client:
//
//	POST /v1/openai/chat/completions  (sync long-poll over POST /v1/jobs)
//	GET  /v1/openai/models            (model IDs the facade accepts)
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
	// is unset. Browser jobs settle in 10-60s; 110s keeps headroom without
	// holding client connections indefinitely.
	defaultFacadeMaxWait = 110 * time.Second
	// minFacadeWaitMs is the smallest per-request ubag_wait_ms the contract
	// accepts; anything positive below it is a client error, not a clamp.
	minFacadeWaitMs = int64(1000)
	// facadeModelSeparator splits "target|setting" model IDs.
	facadeModelSeparator = "|"
	// facadeIdempotencyPrefix namespaces facade-derived native idempotency
	// keys so they can never collide with caller-supplied ones.
	facadeIdempotencyPrefix = "facade-"
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
	// UbagAttachments carries native attachment declarations
	// ({key, content_type, kind, [filename]}) for this facade call. Each
	// declared key MUST be uploaded with PUT
	// /v1/jobs/{ubag_job_id}/artifacts/{key} before the facade wait budget
	// expires; the facade waits on the held job and dispatches it once every
	// declared key is present. Rejected when the resolved target's manifest
	// attachments policy does not accept the declared kind/content_type.
	UbagAttachments []any `json:"ubag_attachments,omitempty"`
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
		catalog := resolveModelCatalog(key)
		settingKeys := make([]string, 0, len(catalog.Settings))
		for name := range catalog.Settings {
			settingKeys = append(settingKeys, name)
		}
		sort.Strings(settingKeys)
		for _, name := range settingKeys {
			setting := catalog.Settings[name]
			if setting.Kind != "choice" {
				continue
			}
			for _, value := range setting.Values {
				models = append(models, openAIFacadeModel{
					ID:      key + facadeModelSeparator + value,
					Object:  "model",
					OwnedBy: "ubag",
				})
			}
		}
	}
	return models
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
	if req.ResponseFormat != nil {
		s.writeFacadeError(w, http.StatusBadRequest, "invalid_request_error", "structured_output_unsupported", "response_format is not supported; strict structured output is not guaranteed")
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

	jobID, status, errType, code, message, ok := s.createFacadeJob(r, req, target, modelSettings, prompt, attachmentDecls)
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
		completion, status, errType, code, message := s.facadeCompletion(req.Model, prompt, job)
		if status != http.StatusOK {
			s.writeFacadeError(w, status, errType, code, message)
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
	catalog := resolveModelCatalog(target)
	settingKeys := make([]string, 0, len(catalog.Settings))
	for name := range catalog.Settings {
		settingKeys = append(settingKeys, name)
	}
	sort.Strings(settingKeys)
	for _, name := range settingKeys {
		setting := catalog.Settings[name]
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
func (s *Server) createFacadeJob(r *http.Request, req openAIFacadeRequest, target string, modelSettings map[string]any, prompt string, attachmentDecls []any) (jobID string, status int, errType, code, message string, ok bool) {
	tenantID, appID := requestScope(r)
	keySeed, err := json.Marshal(map[string]any{
		"model":            req.Model,
		"messages":         facadeMessageDigest(req.Messages),
		"temperature":      req.Temperature,
		"max_tokens":       req.MaxTokens,
		"top_p":            req.TopP,
		"ubag_attachments": attachmentDecls,
	})
	if err != nil {
		return "", http.StatusInternalServerError, "server_error", "job_create_failed", "failed to fingerprint the request", false
	}
	idempotencyKey := facadeIdempotencyPrefix + hashBytes(append([]byte(tenantID+"\n"+appID+"\n"), keySeed...))

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
	input := map[string]any{"prompt": prompt}
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
			if r.Context().Err() != nil {
				if job, ok, _ := s.jobs.Get(context.Background(), jobID); ok {
					s.cancelFacadeJob(context.Background(), job, "facade_client_disconnect")
				}
				return jobstore.Job{}, facadeWaitAbort
			}
			if waitCtx.Err() == context.DeadlineExceeded {
				return jobstore.Job{}, facadeWaitTimeout
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
func (s *Server) facadeCompletion(model, prompt string, job jobstore.Job) (openAIFacadeCompletion, int, string, string, string) {
	switch job.Status {
	case jobstore.StatusCompleted, jobstore.StatusCompletedWithWarnings:
		text := facadeOutputText(buildJobResultEnvelope(job))
		if text == "" {
			return openAIFacadeCompletion{}, http.StatusInternalServerError, "server_error", "empty_completion", "job completed without extractable text"
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
		}, http.StatusOK, "", "", ""
	default:
		signals := s.deriveJobSignals(context.Background(), job)
		detail := signals.ErrorMessage
		if detail == "" {
			detail = fmt.Sprintf("job ended as %s", string(job.Status))
		}
		if signals.ErrorClass == loginRequiredErrorClass {
			return openAIFacadeCompletion{}, http.StatusServiceUnavailable, "provider_error", "provider_login_required", detail
		}
		switch job.Status {
		case jobstore.StatusFailedRetryable:
			return openAIFacadeCompletion{}, http.StatusServiceUnavailable, "provider_error", "provider_transient", detail
		case jobstore.StatusTimedOut:
			return openAIFacadeCompletion{}, http.StatusInternalServerError, "server_error", "provider_timeout", detail
		case jobstore.StatusCanceled:
			return openAIFacadeCompletion{}, http.StatusInternalServerError, "server_error", "job_cancelled", detail
		default:
			return openAIFacadeCompletion{}, http.StatusInternalServerError, "server_error", "provider_failed", detail
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
