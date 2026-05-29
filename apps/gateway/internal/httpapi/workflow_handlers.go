package httpapi

import (
	"net/http"
	"strings"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/payloadpolicy"
	"github.com/ubag/ubag/apps/gateway/internal/workflow"
)

type workflowStepPayload struct {
	ID              string         `json:"id,omitempty"`
	Target          string         `json:"target"`
	Command         string         `json:"command"`
	TemplateID      string         `json:"template_id,omitempty"`
	Input           map[string]any `json:"input,omitempty"`
	ContinueOnError bool           `json:"continue_on_error,omitempty"`
}

type createWorkflowRequest struct {
	APIVersion string                `json:"api_version,omitempty"`
	Name       string                `json:"name"`
	Steps      []workflowStepPayload `json:"steps"`
}

type createWorkflowRunRequest struct {
	APIVersion string `json:"api_version,omitempty"`
}

type workflowDefinitionResponse struct {
	APIVersion string                `json:"api_version"`
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Steps      []workflowStepPayload `json:"steps"`
	CreatedAt  time.Time             `json:"created_at"`
	TraceID    string                `json:"trace_id"`
}

type workflowStepRunPayload struct {
	StepID      string     `json:"step_id"`
	State       string     `json:"state"`
	JobID       string     `json:"job_id,omitempty"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type workflowRunResponse struct {
	APIVersion   string                   `json:"api_version"`
	ID           string                   `json:"id"`
	DefinitionID string                   `json:"definition_id"`
	State        string                   `json:"state"`
	CurrentStep  int                      `json:"current_step"`
	Steps        []workflowStepRunPayload `json:"steps"`
	CreatedAt    time.Time                `json:"created_at"`
	UpdatedAt    time.Time                `json:"updated_at"`
	TraceID      string                   `json:"trace_id"`
}

// handleWorkflows serves the /v1/workflows collection (list + create).
func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listWorkflows(w, r)
	case http.MethodPost:
		s.createWorkflow(w, r)
	default:
		s.writeMethodNotAllowed(w, r, http.MethodGet, http.MethodPost)
	}
}

// handleWorkflowsSubtree serves nested workflow routes:
//
//	POST /v1/workflows/{id}/runs   create + advance a run
//	GET  /v1/workflows/runs/{id}   fetch a run
func (s *Server) handleWorkflowsSubtree(w http.ResponseWriter, r *http.Request) {
	tail := splitRouteTail(r.URL.Path, "/v1/workflows/")
	switch {
	case len(tail) == 1 && tail[0] == "runs":
		s.writeNotFound(w, r)
	case len(tail) == 2 && tail[0] == "runs" && r.Method == http.MethodGet:
		s.getWorkflowRun(w, r, tail[1])
	case len(tail) == 2 && tail[0] == "runs":
		s.writeMethodNotAllowed(w, r, http.MethodGet)
	case len(tail) == 2 && tail[1] == "runs" && r.Method == http.MethodPost:
		s.createWorkflowRun(w, r, tail[0])
	case len(tail) == 2 && tail[1] == "runs":
		s.writeMethodNotAllowed(w, r, http.MethodPost)
	default:
		s.writeNotFound(w, r)
	}
}

func (s *Server) listWorkflows(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	traceID := traceIDFromContext(r.Context())
	if s.workflows == nil {
		s.writeJSON(w, http.StatusOK, collectionResponse{
			APIVersion: s.apiVersion,
			Kind:       "workflows",
			Data:       []map[string]any{},
			TraceID:    traceID,
		})
		return
	}
	tenantID, appID := requestScope(r)
	limit, ok := s.parseLimit(w, r, r.URL.Query().Get("limit"), 100)
	if !ok {
		return
	}
	defs, err := s.workflows.ListDefinitions(r.Context(), tenantID, appID, limit)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to list workflow definitions"))
		return
	}
	data := make([]map[string]any, 0, len(defs))
	for _, def := range defs {
		data = append(data, map[string]any{
			"id":         def.ID,
			"name":       def.Name,
			"step_count": len(def.Steps),
			"created_at": def.CreatedAt.UTC(),
		})
	}
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "workflows",
		Data:       data,
		TraceID:    traceID,
	})
}

func (s *Server) createWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		s.writeNotImplemented(w, r, "workflow orchestration is not configured")
		return
	}
	raw, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var request createWorkflowRequest
	if !s.decodeBody(w, r, raw, &request) {
		return
	}
	apiVersion, ok := s.resolveAPIVersion(w, r, request.APIVersion)
	if !ok {
		return
	}
	if strings.TrimSpace(request.Name) == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WORKFLOW-NAME-001", "name is required"))
		return
	}
	if len(request.Steps) == 0 {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WORKFLOW-STEPS-001", "at least one step is required"))
		return
	}
	idempotencyKey, ok := s.requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}

	steps := make([]workflow.Step, 0, len(request.Steps))
	for i, step := range request.Steps {
		if !isTargetKey(step.Target) {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WORKFLOW-STEP-TARGET-001", "step.target is required and must match ^[a-z0-9][a-z0-9._-]*$"))
			return
		}
		if strings.TrimSpace(step.Command) == "" {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WORKFLOW-STEP-COMMAND-001", "step.command is required"))
			return
		}
		if step.Input != nil {
			if err := payloadpolicy.Validate(step.Input); err != nil {
				s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WORKFLOW-STEP-PAYLOAD-SAFETY-001", err.Error()))
				return
			}
		}
		steps = append(steps, workflow.Step{
			ID:              strings.TrimSpace(step.ID),
			Target:          strings.TrimSpace(step.Target),
			Command:         strings.TrimSpace(step.Command),
			TemplateID:      strings.TrimSpace(step.TemplateID),
			Input:           step.Input,
			ContinueOnError: step.ContinueOnError,
		})
		_ = i
	}

	tenantID, appID := requestScope(r)
	def, err := s.workflows.CreateDefinition(r.Context(), workflow.Definition{
		TenantID: tenantID,
		AppID:    appID,
		Name:     strings.TrimSpace(request.Name),
		Steps:    steps,
	})
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WORKFLOW-DEFINITION-001", err.Error()))
		return
	}
	_ = idempotencyKey

	w.Header().Set("Location", "/v1/workflows/"+def.ID)
	s.writeJSON(w, http.StatusCreated, s.workflowDefinitionToResponse(apiVersion, def, traceIDFromContext(r.Context())))
}

func (s *Server) createWorkflowRun(w http.ResponseWriter, r *http.Request, definitionID string) {
	if s.workflows == nil || s.workflowEngine == nil {
		s.writeNotImplemented(w, r, "workflow orchestration is not configured")
		return
	}
	raw, hasBody, ok := s.readOptionalBody(w, r)
	if !ok {
		return
	}
	var request createWorkflowRunRequest
	if hasBody {
		if !s.decodeBody(w, r, raw, &request) {
			return
		}
	}
	apiVersion, ok := s.resolveAPIVersion(w, r, request.APIVersion)
	if !ok {
		return
	}
	idempotencyKey, ok := s.requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:create") {
		return
	}

	tenantID, appID := requestScope(r)
	def, found, err := s.workflows.GetDefinition(r.Context(), tenantID, appID, definitionID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load workflow definition"))
		return
	}
	if !found {
		s.writeNotFound(w, r)
		return
	}

	run, err := s.workflows.CreateRun(r.Context(), workflow.Run{
		DefinitionID:   def.ID,
		TenantID:       tenantID,
		AppID:          appID,
		IdempotencyKey: idempotencyKey,
		Steps:          workflow.NewStepRuns(def),
	})
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-WORKFLOW-RUN-001", err.Error()))
		return
	}

	// Dispatch reuses the job store + executor directly (NOT the HTTP job
	// handler) so each workflow step becomes a regular gateway job. Per-step
	// idempotency keys keep re-advances and retries safe.
	dispatch := s.workflowDispatcher(r, run.ID, def)
	if err := s.workflowEngine.Advance(r.Context(), &run, dispatch); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to advance workflow run"))
		return
	}

	w.Header().Set("Location", "/v1/workflows/runs/"+run.ID)
	s.writeJSON(w, http.StatusAccepted, s.workflowRunToResponse(apiVersion, run, traceIDFromContext(r.Context())))
}

func (s *Server) getWorkflowRun(w http.ResponseWriter, r *http.Request, runID string) {
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	if s.workflows == nil {
		s.writeNotImplemented(w, r, "workflow orchestration is not configured")
		return
	}
	tenantID, appID := requestScope(r)
	run, found, err := s.workflows.GetRun(r.Context(), tenantID, appID, runID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, internalError("failed to load workflow run"))
		return
	}
	if !found {
		s.writeNotFound(w, r)
		return
	}
	s.writeJSON(w, http.StatusOK, s.workflowRunToResponse(s.apiVersion, run, traceIDFromContext(r.Context())))
}

// workflowDispatcher builds a DispatchFunc that enqueues a gateway job per step.
func (s *Server) workflowDispatcher(r *http.Request, runID string, def workflow.Definition) workflow.DispatchFunc {
	ctx := r.Context()
	traceID := traceIDFromContext(ctx)
	client := map[string]any{
		"app_id":      s.appID,
		"app_version": firstNonEmpty(s.version, "0.0.0"),
		"sdk": map[string]any{
			"name":    "ubag-gateway-workflow",
			"version": firstNonEmpty(s.version, "0.0.0"),
		},
	}
	return func(step workflow.Step) (string, error) {
		if step.Input != nil {
			if err := payloadpolicy.Validate(step.Input); err != nil {
				return "", err
			}
		}
		input := step.Input
		if input == nil {
			input = map[string]any{}
		}
		job, err := s.jobs.Create(ctx, jobstore.CreateRequest{
			APIVersion:     s.apiVersion,
			TenantID:       def.TenantID,
			AppID:          def.AppID,
			IdempotencyKey: "wf_" + runID + "_" + step.ID,
			Target:         step.Target,
			CommandType:    step.Command,
			Client:         client,
			TemplateID:     step.TemplateID,
			Input:          input,
			TraceID:        traceID,
		})
		if err != nil {
			return "", err
		}
		if _, err := s.executor.EnqueueJob(ctx, job); err != nil {
			_, _, _ = s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusFailedRetryable)
			return "", err
		}
		return job.ID, nil
	}
}

func (s *Server) workflowDefinitionToResponse(apiVersion string, def workflow.Definition, traceID string) workflowDefinitionResponse {
	steps := make([]workflowStepPayload, 0, len(def.Steps))
	for _, step := range def.Steps {
		steps = append(steps, workflowStepPayload{
			ID:              step.ID,
			Target:          step.Target,
			Command:         step.Command,
			TemplateID:      step.TemplateID,
			Input:           step.Input,
			ContinueOnError: step.ContinueOnError,
		})
	}
	return workflowDefinitionResponse{
		APIVersion: apiVersion,
		ID:         def.ID,
		Name:       def.Name,
		Steps:      steps,
		CreatedAt:  def.CreatedAt.UTC(),
		TraceID:    traceID,
	}
}

func (s *Server) workflowRunToResponse(apiVersion string, run workflow.Run, traceID string) workflowRunResponse {
	steps := make([]workflowStepRunPayload, 0, len(run.Steps))
	for _, sr := range run.Steps {
		payload := workflowStepRunPayload{
			StepID: sr.StepID,
			State:  string(sr.State),
			JobID:  sr.JobID,
			Error:  sr.Error,
		}
		if !sr.StartedAt.IsZero() {
			started := sr.StartedAt.UTC()
			payload.StartedAt = &started
		}
		if !sr.CompletedAt.IsZero() {
			completed := sr.CompletedAt.UTC()
			payload.CompletedAt = &completed
		}
		steps = append(steps, payload)
	}
	return workflowRunResponse{
		APIVersion:   apiVersion,
		ID:           run.ID,
		DefinitionID: run.DefinitionID,
		State:        string(run.State),
		CurrentStep:  run.CurrentStep,
		Steps:        steps,
		CreatedAt:    run.CreatedAt.UTC(),
		UpdatedAt:    run.UpdatedAt.UTC(),
		TraceID:      traceID,
	}
}
