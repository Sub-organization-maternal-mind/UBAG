// Package grpcapi implements the UBAG gateway JobService over gRPC (and, via
// the wrapper mounted in cmd/gateway, gRPC-Web). It mirrors the HTTP job API:
// the same stores, idempotency service, and executor are reused, and the
// protocol-agnostic business rules live in the shared jobcore package so that
// HTTP and gRPC accept and reject identical job payloads.
package grpcapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	"github.com/ubag/ubag/apps/gateway/internal/jobcore"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/webhooks"
	ubagv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/v1"
)

var (
	apiVersionPattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{16,128}$`)
	targetPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

const streamPollLimit = 100

// Config wires the gRPC JobService to the same dependencies as the HTTP server.
type Config struct {
	APIVersion string
	AppSecret  string
	TenantID   string
	AppID      string
	ActorRole  string

	Jobs        jobstore.Store
	Idempotency idempotency.Service
	Executor    executor.Dispatcher
}

// Server implements ubagv1.JobServiceServer.
type Server struct {
	ubagv1.UnimplementedJobServiceServer

	apiVersion  string
	appSecret   string
	tenantID    string
	appID       string
	actorRole   string
	jobs        jobstore.Store
	idempotency idempotency.Service
	executor    executor.Dispatcher
}

// NewServer constructs a JobService gRPC server from the supplied config.
func NewServer(config Config) *Server {
	return &Server{
		apiVersion:  config.APIVersion,
		appSecret:   config.AppSecret,
		tenantID:    config.TenantID,
		appID:       config.AppID,
		actorRole:   config.ActorRole,
		jobs:        config.Jobs,
		idempotency: config.Idempotency,
		executor:    config.Executor,
	}
}

// CreateJob validates, reserves idempotency, persists, and enqueues a job.
func (s *Server) CreateJob(ctx context.Context, req *ubagv1.CreateJobRequest) (*ubagv1.JobResponse, error) {
	if err := s.authorize(ctx, "job:create"); err != nil {
		return nil, err
	}
	apiVersion, err := s.resolveAPIVersion(req.GetApiVersion())
	if err != nil {
		return nil, err
	}

	idempotencyKey := strings.TrimSpace(req.GetIdempotencyKey())
	if idempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required for job creation")
	}
	if !idempotencyKeyPattern.MatchString(idempotencyKey) {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key must be 16-128 characters and contain only letters, numbers, dot, underscore, colon, or dash")
	}

	clientMsg := req.GetClient()
	spec := req.GetJob()
	if clientMsg == nil || spec == nil {
		return nil, status.Error(codes.InvalidArgument, "client and job are required")
	}
	if !targetPattern.MatchString(strings.TrimSpace(spec.GetTarget())) {
		return nil, status.Error(codes.InvalidArgument, "job.target is required and must match ^[a-z0-9][a-z0-9._-]*$")
	}
	if !targetPattern.MatchString(strings.TrimSpace(spec.GetCommandType())) {
		return nil, status.Error(codes.InvalidArgument, "job.command_type is required and must match ^[a-z0-9][a-z0-9._-]*$")
	}
	if strings.TrimSpace(clientMsg.GetAppId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "client.app_id is required")
	}
	if strings.TrimSpace(clientMsg.GetAppVersion()) == "" {
		return nil, status.Error(codes.InvalidArgument, "client.app_version is required")
	}
	if strings.TrimSpace(clientMsg.GetSdkName()) == "" || strings.TrimSpace(clientMsg.GetSdkVersion()) == "" {
		return nil, status.Error(codes.InvalidArgument, "client.sdk_name and client.sdk_version are required")
	}

	input, err := decodeJSONObject(spec.GetInputJson(), "input")
	if err != nil {
		return nil, err
	}
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "job.input is required and must be a JSON object")
	}
	options, err := decodeJSONObject(spec.GetOptionsJson(), "options")
	if err != nil {
		return nil, err
	}
	callbacks, err := decodeJSONObject(spec.GetCallbacksJson(), "callbacks")
	if err != nil {
		return nil, err
	}
	jobContext, err := decodeJSONObject(spec.GetContextJson(), "context")
	if err != nil {
		return nil, err
	}

	client := jobcore.Client{
		AppID:      clientMsg.GetAppId(),
		AppVersion: clientMsg.GetAppVersion(),
		DeviceID:   clientMsg.GetDeviceId(),
		UserRef:    clientMsg.GetUserRef(),
		SDKName:    clientMsg.GetSdkName(),
		SDKVersion: clientMsg.GetSdkVersion(),
	}
	jobSpec := jobcore.Spec{
		Target:         spec.GetTarget(),
		CommandType:    spec.GetCommandType(),
		ConversationID: spec.GetConversationId(),
		TemplateID:     spec.GetTemplateId(),
		Input:          input,
		Options:        options,
		Callbacks:      callbacks,
		Context:        jobContext,
	}
	if err := jobcore.ValidatePayload(client, jobSpec); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	requestHash, err := jobcore.CanonicalCreateHash(apiVersion, client, jobSpec)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "request payload must be valid JSON")
	}

	scope := idempotency.Scope{
		TenantID:  s.tenantID,
		AppID:     s.appID,
		Operation: "create_job",
		Key:       idempotencyKey,
	}
	decision, err := s.idempotency.Reserve(ctx, scope, requestHash)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to reserve idempotency key")
	}
	switch decision.Kind {
	case idempotency.DecisionConflict:
		return nil, status.Error(codes.AlreadyExists, "idempotency key was replayed with a different payload")
	case idempotency.DecisionReplay:
		return s.replayJob(ctx, decision.Record)
	}

	job, err := s.jobs.Create(ctx, jobstore.CreateRequest{
		APIVersion:     apiVersion,
		TenantID:       s.tenantID,
		AppID:          s.appID,
		IdempotencyKey: idempotencyKey,
		Target:         strings.TrimSpace(spec.GetTarget()),
		CommandType:    strings.TrimSpace(spec.GetCommandType()),
		Client:         jobcore.ClientToMap(client),
		ConversationID: strings.TrimSpace(spec.GetConversationId()),
		TemplateID:     strings.TrimSpace(spec.GetTemplateId()),
		Input:          input,
		Options:        options,
		Callbacks:      callbacks,
		Context:        jobContext,
		TraceID:        traceID(),
	})
	if err != nil {
		_ = s.idempotency.Release(ctx, scope)
		return nil, status.Error(codes.Internal, "failed to create job")
	}
	if _, err := s.executor.EnqueueJob(ctx, job); err != nil {
		_, _, _ = s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusFailedRetryable)
		_ = s.idempotency.Release(ctx, scope)
		return nil, status.Error(codes.Unavailable, "failed to enqueue job for execution")
	}
	if err := s.idempotency.Complete(ctx, scope, job.ID, 202); err != nil {
		return nil, status.Error(codes.Internal, "failed to complete idempotency record")
	}

	return jobToResponse(job, false), nil
}

// GetJob returns a single job scoped to the configured tenant/app.
func (s *Server) GetJob(ctx context.Context, req *ubagv1.GetJobRequest) (*ubagv1.JobResponse, error) {
	if err := s.authorize(ctx, "job:read"); err != nil {
		return nil, err
	}
	job, err := s.loadJob(ctx, req.GetJobId())
	if err != nil {
		return nil, err
	}
	return jobToResponse(job, false), nil
}

// ListJobs returns jobs scoped to the configured tenant/app.
func (s *Server) ListJobs(ctx context.Context, req *ubagv1.ListJobsRequest) (*ubagv1.ListJobsResponse, error) {
	if err := s.authorize(ctx, "job:read"); err != nil {
		return nil, err
	}
	statusFilter := strings.TrimSpace(req.GetStatus())
	if statusFilter != "" && !jobstore.KnownStatus(jobstore.Status(statusFilter)) {
		return nil, status.Error(codes.InvalidArgument, "status filter is not supported")
	}
	target := strings.TrimSpace(req.GetTarget())
	if target != "" && !targetPattern.MatchString(target) {
		return nil, status.Error(codes.InvalidArgument, "target filter must match ^[a-z0-9][a-z0-9._-]*$")
	}
	sortParam := strings.TrimSpace(req.GetSort())
	if sortParam != "" && sortParam != "created_at" && sortParam != "-created_at" {
		return nil, status.Error(codes.InvalidArgument, "sort must be created_at or -created_at")
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		return nil, status.Error(codes.InvalidArgument, "limit must be an integer from 1 to 100")
	}

	jobs, err := s.jobs.List(ctx, jobstore.ListFilter{
		TenantID: s.tenantID,
		AppID:    s.appID,
		Status:   statusFilter,
		Target:   target,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list jobs")
	}
	sortJobs(jobs, sortParam)
	if cursor := strings.TrimSpace(req.GetCursor()); cursor != "" {
		jobs = jobsAfterCursor(jobs, cursor)
	}
	var nextCursor string
	if len(jobs) > limit {
		nextCursor = jobs[limit-1].ID
		jobs = jobs[:limit]
	}

	responses := make([]*ubagv1.JobResponse, 0, len(jobs))
	for _, job := range jobs {
		responses = append(responses, jobToResponse(job, false))
	}
	return &ubagv1.ListJobsResponse{
		ApiVersion: s.apiVersion,
		Jobs:       responses,
		NextCursor: nextCursor,
		TraceId:    traceID(),
	}, nil
}

// CancelJob cancels an existing job.
func (s *Server) CancelJob(ctx context.Context, req *ubagv1.JobMutationRequest) (*ubagv1.JobResponse, error) {
	if err := s.authorize(ctx, "job:cancel"); err != nil {
		return nil, err
	}
	existing, err := s.loadJob(ctx, req.GetJobId())
	if err != nil {
		return nil, err
	}
	scope, record, replay, err := s.reserveMutation(ctx, "cancel_job", existing.ID, req)
	if err != nil {
		return nil, err
	}
	if replay {
		return s.replayJob(ctx, record)
	}

	reason := strings.TrimSpace(req.GetReason())
	if reason == "" {
		reason = "caller_cancelled"
	}
	if err := s.executor.CancelJob(ctx, existing, reason); err != nil {
		_ = s.idempotency.Release(ctx, scope)
		return nil, status.Error(codes.Unavailable, "failed to cancel job execution")
	}
	job, found, err := s.jobs.UpdateStatus(ctx, existing.ID, jobstore.StatusCanceled)
	if err != nil {
		_ = s.idempotency.Release(ctx, scope)
		return nil, status.Error(codes.Internal, "failed to cancel job")
	}
	if !found {
		_ = s.idempotency.Release(ctx, scope)
		return nil, status.Error(codes.NotFound, "job was not found")
	}
	if err := s.idempotency.Complete(ctx, scope, job.ID, 202); err != nil {
		return nil, status.Error(codes.Internal, "failed to complete idempotency record")
	}
	return jobToResponse(job, false), nil
}

// RetryJob clones a job and enqueues the copy for execution.
func (s *Server) RetryJob(ctx context.Context, req *ubagv1.JobMutationRequest) (*ubagv1.JobResponse, error) {
	if err := s.authorize(ctx, "job:retry"); err != nil {
		return nil, err
	}
	original, err := s.loadJob(ctx, req.GetJobId())
	if err != nil {
		return nil, err
	}
	scope, record, replay, err := s.reserveMutation(ctx, "retry_job", original.ID, req)
	if err != nil {
		return nil, err
	}
	if replay {
		return s.replayJob(ctx, record)
	}

	job, err := s.jobs.Create(ctx, jobstore.CreateRequest{
		APIVersion:     original.APIVersion,
		TenantID:       original.TenantID,
		AppID:          original.AppID,
		IdempotencyKey: scope.Key,
		Target:         original.Target,
		CommandType:    original.CommandType,
		Client:         original.Client,
		ConversationID: original.ConversationID,
		TemplateID:     original.TemplateID,
		Input:          original.Input,
		Options:        original.Options,
		Callbacks:      original.Callbacks,
		Context:        original.Context,
		TraceID:        traceID(),
		RetryOf:        original.ID,
	})
	if err != nil {
		_ = s.idempotency.Release(ctx, scope)
		return nil, status.Error(codes.Internal, "failed to retry job")
	}
	if _, err := s.executor.EnqueueJob(ctx, job); err != nil {
		_, _, _ = s.jobs.UpdateStatus(ctx, job.ID, jobstore.StatusFailedRetryable)
		_ = s.idempotency.Release(ctx, scope)
		return nil, status.Error(codes.Unavailable, "failed to enqueue retry job for execution")
	}
	if err := s.idempotency.Complete(ctx, scope, job.ID, 202); err != nil {
		return nil, status.Error(codes.Internal, "failed to complete idempotency record")
	}
	return jobToResponse(job, false), nil
}

// ListJobEvents returns a page of job lifecycle events.
func (s *Server) ListJobEvents(ctx context.Context, req *ubagv1.ListJobEventsRequest) (*ubagv1.ListJobEventsResponse, error) {
	if err := s.authorize(ctx, "job:read"); err != nil {
		return nil, err
	}
	job, err := s.loadJob(ctx, req.GetJobId())
	if err != nil {
		return nil, err
	}
	afterSequence := int(req.GetAfterSequence())
	if afterSequence < 0 {
		return nil, status.Error(codes.InvalidArgument, "after_sequence must be a non-negative integer")
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		return nil, status.Error(codes.InvalidArgument, "limit must be an integer from 1 to 100")
	}

	events, found, err := s.jobs.ListEvents(ctx, job.ID, afterSequence, limit+1)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to load job events")
	}
	if !found {
		return nil, status.Error(codes.NotFound, "job was not found")
	}
	var nextCursor string
	if len(events) > limit {
		nextCursor = fmt.Sprintf("%d", events[limit-1].Sequence)
		events = events[:limit]
	}
	responses := make([]*ubagv1.JobEvent, 0, len(events))
	for _, event := range events {
		responses = append(responses, eventToResponse(event))
	}
	return &ubagv1.ListJobEventsResponse{
		ApiVersion: job.APIVersion,
		JobId:      job.ID,
		Events:     responses,
		NextCursor: nextCursor,
		TraceId:    traceID(),
	}, nil
}

// StreamJobEvents long-polls the store and streams events until the job reaches
// a terminal status or the stream context is cancelled.
func (s *Server) StreamJobEvents(req *ubagv1.ListJobEventsRequest, stream ubagv1.JobService_StreamJobEventsServer) error {
	ctx := stream.Context()
	if err := s.authorize(ctx, "job:read"); err != nil {
		return err
	}
	job, err := s.loadJob(ctx, req.GetJobId())
	if err != nil {
		return err
	}

	afterSequence := int(req.GetAfterSequence())
	if afterSequence < 0 {
		return status.Error(codes.InvalidArgument, "after_sequence must be a non-negative integer")
	}

	for {
		if ctx.Err() != nil {
			return status.FromContextError(ctx.Err()).Err()
		}
		events, found, err := s.jobs.WaitEvents(ctx, job.ID, afterSequence, streamPollLimit)
		if err != nil {
			if ctx.Err() != nil {
				return status.FromContextError(ctx.Err()).Err()
			}
			return status.Error(codes.Internal, "failed to stream job events")
		}
		if !found {
			return status.Error(codes.NotFound, "job was not found")
		}
		terminal := false
		for _, event := range events {
			if err := stream.Send(eventToResponse(event)); err != nil {
				return err
			}
			afterSequence = event.Sequence
			if isTerminalEvent(event) {
				terminal = true
			}
		}
		if terminal {
			return nil
		}
		if current, ok, err := s.jobs.Get(ctx, job.ID); err == nil && ok && jobstore.TerminalStatus(current.Status) {
			return nil
		}
	}
}

func (s *Server) reserveMutation(ctx context.Context, operation, resourceID string, req *ubagv1.JobMutationRequest) (idempotency.Scope, idempotency.Record, bool, error) {
	if jobID := strings.TrimSpace(req.GetJobId()); jobID != "" && jobID != resourceID {
		return idempotency.Scope{}, idempotency.Record{}, false, status.Error(codes.InvalidArgument, "request job_id must match the target job_id")
	}
	if version := strings.TrimSpace(req.GetApiVersion()); version != "" && !s.isSupportedAPIVersion(version) {
		return idempotency.Scope{}, idempotency.Record{}, false, status.Error(codes.InvalidArgument, "requested API version is not supported")
	}
	idempotencyKey := strings.TrimSpace(req.GetIdempotencyKey())
	if idempotencyKey == "" {
		return idempotency.Scope{}, idempotency.Record{}, false, status.Error(codes.InvalidArgument, "idempotency_key is required for mutating job operations")
	}
	if !idempotencyKeyPattern.MatchString(idempotencyKey) {
		return idempotency.Scope{}, idempotency.Record{}, false, status.Error(codes.InvalidArgument, "idempotency_key must be 16-128 characters and contain only letters, numbers, dot, underscore, colon, or dash")
	}

	scope := idempotency.Scope{
		TenantID:  s.tenantID,
		AppID:     s.appID,
		Operation: operation,
		Key:       idempotencyKey,
	}
	requestHash := canonicalMutationHash(operation, resourceID, req)
	decision, err := s.idempotency.Reserve(ctx, scope, requestHash)
	if err != nil {
		return idempotency.Scope{}, idempotency.Record{}, false, status.Error(codes.Internal, "failed to reserve idempotency key")
	}
	switch decision.Kind {
	case idempotency.DecisionConflict:
		return idempotency.Scope{}, idempotency.Record{}, false, status.Error(codes.AlreadyExists, "idempotency key was replayed with a different payload")
	case idempotency.DecisionReplay:
		return scope, decision.Record, true, nil
	default:
		return scope, decision.Record, false, nil
	}
}

func (s *Server) replayJob(ctx context.Context, record idempotency.Record) (*ubagv1.JobResponse, error) {
	if record.ResourceID == "" {
		return nil, status.Error(codes.AlreadyExists, "idempotent operation is still in progress")
	}
	job, ok, err := s.jobs.Get(ctx, record.ResourceID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to load idempotent job")
	}
	if !ok {
		return nil, status.Error(codes.Internal, "idempotency record points to a missing job")
	}
	return jobToResponse(job, true), nil
}

func (s *Server) loadJob(ctx context.Context, id string) (jobstore.Job, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return jobstore.Job{}, status.Error(codes.InvalidArgument, "job_id is required")
	}
	if scoped, ok := s.jobs.(jobstore.ScopedStore); ok {
		job, found, err := scoped.GetScoped(ctx, id, s.tenantID, s.appID)
		if err != nil {
			return jobstore.Job{}, status.Error(codes.Internal, "failed to load job")
		}
		if !found {
			return jobstore.Job{}, status.Error(codes.NotFound, "job was not found")
		}
		return job, nil
	}
	job, found, err := s.jobs.Get(ctx, id)
	if err != nil {
		return jobstore.Job{}, status.Error(codes.Internal, "failed to load job")
	}
	if !found || job.TenantID != s.tenantID || job.AppID != s.appID {
		return jobstore.Job{}, status.Error(codes.NotFound, "job was not found")
	}
	return job, nil
}

func (s *Server) resolveAPIVersion(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return s.apiVersion, nil
	}
	if !s.isSupportedAPIVersion(requested) {
		return "", status.Error(codes.InvalidArgument, "requested API version is not supported")
	}
	return requested, nil
}

func (s *Server) isSupportedAPIVersion(value string) bool {
	return apiVersionPattern.MatchString(value) && value == s.apiVersion
}

func (s *Server) authorize(ctx context.Context, action string) error {
	if !s.authenticate(ctx) {
		return status.Error(codes.Unauthenticated, "missing or invalid app-secret bearer token")
	}
	if !allowGatewayAction(s.actorRole, action) {
		return status.Error(codes.PermissionDenied, "actor role is not allowed to perform this action")
	}
	return nil
}

func (s *Server) authenticate(ctx context.Context) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	for _, value := range md.Get("authorization") {
		if validBearerToken(value, s.appSecret) {
			return true
		}
	}
	return false
}

func decodeJSONObject(raw string, field string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("job.%s must be a valid JSON object", field))
	}
	return out, nil
}

func canonicalMutationHash(operation, resourceID string, req *ubagv1.JobMutationRequest) string {
	var metadataMap map[string]any
	if raw := strings.TrimSpace(req.GetMetadataJson()); raw != "" {
		_ = json.Unmarshal([]byte(raw), &metadataMap)
	}
	payload := map[string]any{
		"operation":   operation,
		"resource_id": resourceID,
		"api_version": strings.TrimSpace(req.GetApiVersion()),
		"job_id":      strings.TrimSpace(req.GetJobId()),
		"reason":      strings.TrimSpace(req.GetReason()),
		"metadata":    metadataMap,
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

func jobToResponse(job jobstore.Job, replay bool) *ubagv1.JobResponse {
	metadata := map[string]any{
		"command_type": job.CommandType,
		"app_id":       job.AppID,
		"tenant_id":    job.TenantID,
	}
	if job.ConversationID != "" {
		metadata["conversation_id"] = job.ConversationID
	}
	if job.TemplateID != "" {
		metadata["template_id"] = job.TemplateID
	}
	if job.Client != nil {
		metadata["client"] = job.Client
	}
	if job.Input != nil {
		metadata["input"] = job.Input
	}
	if job.Options != nil {
		metadata["options"] = job.Options
	}
	if job.Callbacks != nil {
		metadata["callbacks"] = webhooks.RedactCallbacks(job.Callbacks)
	}
	if job.Context != nil {
		metadata["context"] = job.Context
	}
	if job.RetryOf != "" {
		metadata["retry_of"] = job.RetryOf
	}

	return &ubagv1.JobResponse{
		ApiVersion:       job.APIVersion,
		JobId:            job.ID,
		IdempotentReplay: replay,
		Status:           string(job.Status),
		Target:           job.Target,
		ResultJson:       marshalJSON(job.Result),
		MetadataJson:     marshalJSON(metadata),
		TraceId:          traceID(),
		EventsUrl:        fmt.Sprintf("/v1/jobs/%s/events", job.ID),
	}
}

func eventToResponse(event jobstore.Event) *ubagv1.JobEvent {
	return &ubagv1.JobEvent{
		EventId:    event.ID,
		JobId:      event.JobID,
		ApiVersion: event.APIVersion,
		Type:       event.Type,
		CreatedAt:  event.CreatedAt.UTC().Format(time.RFC3339Nano),
		Sequence:   int32(event.Sequence),
		DataJson:   marshalJSON(event.Data),
		TraceId:    event.TraceID,
	}
}

func marshalJSON(value any) string {
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func isTerminalEvent(event jobstore.Event) bool {
	if raw, ok := event.Data["status"].(string); ok {
		return jobstore.TerminalStatus(jobstore.Status(raw))
	}
	return false
}

func sortJobs(jobs []jobstore.Job, sortParam string) {
	descending := sortParam == "" || sortParam == "-created_at"
	sort.SliceStable(jobs, func(left, right int) bool {
		if descending {
			return jobs[left].CreatedAt.After(jobs[right].CreatedAt)
		}
		return jobs[left].CreatedAt.Before(jobs[right].CreatedAt)
	})
}

func jobsAfterCursor(jobs []jobstore.Job, cursor string) []jobstore.Job {
	for index, job := range jobs {
		if job.ID == cursor {
			return jobs[index+1:]
		}
	}
	return jobs
}

func allowGatewayAction(role, action string) bool {
	switch role {
	case "developer":
		return action == "job:create" || action == "job:read" || action == "job:cancel" || action == "job:retry"
	case "operator", "service":
		return action == "job:create" || action == "job:read" || action == "job:cancel" || action == "job:retry"
	case "admin":
		return action == "job:create" || action == "job:read" || action == "job:cancel" || action == "job:retry"
	case "superadmin":
		return true
	case "viewer":
		return action == "job:read"
	default:
		return false
	}
}

func validBearerToken(header string, expectedSecret string) bool {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	return constantTimeEqual(parts[1], expectedSecret)
}

func constantTimeEqual(actual, expected string) bool {
	actualHash := sha256.Sum256([]byte(actual))
	expectedHash := sha256.Sum256([]byte(expected))
	sameLength := subtle.ConstantTimeEq(int32(len(actual)), int32(len(expected)))
	sameValue := subtle.ConstantTimeCompare(actualHash[:], expectedHash[:])
	return sameLength&sameValue == 1
}

func traceID() string {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return fmt.Sprintf("trace_%d", time.Now().UTC().UnixNano())
	}
	return "trace_" + hex.EncodeToString(buffer[:])
}
