package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/payloadpolicy"
)

type MemoryStore struct {
	mu             sync.Mutex
	cond           *sync.Cond
	now            func() time.Time
	sequence       uint64
	eventSeqGlobal uint64
	eventSeq       map[string]int
	eventKey       map[string]map[string]struct{}
	jobs           map[string]Job
	events         map[string][]Event
	order          []string
}

func NewMemoryStore() *MemoryStore {
	store := &MemoryStore{
		now:      time.Now,
		eventSeq: make(map[string]int),
		eventKey: make(map[string]map[string]struct{}),
		jobs:     make(map[string]Job),
		events:   make(map[string][]Event),
	}
	store.cond = sync.NewCond(&store.mu)
	return store
}

func (m *MemoryStore) Create(_ context.Context, request CreateRequest) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sequence++
	now := m.now().UTC()
	id := fmt.Sprintf("job_%012d", m.sequence)

	status := StatusQueued
	if request.NotBefore != nil && request.NotBefore.After(now) {
		status = StatusScheduled
	}
	job := Job{
		ID:             id,
		APIVersion:     request.APIVersion,
		TenantID:       request.TenantID,
		AppID:          request.AppID,
		IdempotencyKey: request.IdempotencyKey,
		Target:         request.Target,
		CommandType:    request.CommandType,
		Client:         cloneMap(request.Client),
		ConversationID: request.ConversationID,
		TemplateID:     request.TemplateID,
		Input:          cloneMap(request.Input),
		Options:        cloneMap(request.Options),
		Callbacks:      cloneMap(request.Callbacks),
		Context:        cloneMap(request.Context),
		Status:         status,
		TraceID:        request.TraceID,
		RetryOf:        request.RetryOf,
		NotBefore:      request.NotBefore,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	m.jobs[id] = job
	m.order = append(m.order, id)
	m.appendEventLocked(job, "queued", map[string]any{
		"status":       string(job.Status),
		"target":       job.Target,
		"command_type": job.CommandType,
	})

	return job, nil
}

func (m *MemoryStore) Get(_ context.Context, id string) (Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[id]
	return job, ok, nil
}

func (m *MemoryStore) GetScoped(_ context.Context, id string, tenantID string, appID string) (Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[id]
	if !ok || job.TenantID != tenantID || job.AppID != appID {
		return Job{}, false, nil
	}
	return job, true, nil
}

func (m *MemoryStore) List(_ context.Context, filter ListFilter) ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	jobs := make([]Job, 0, len(m.order))
	for _, id := range m.order {
		job := m.jobs[id]
		if filter.TenantID != "" && job.TenantID != filter.TenantID {
			continue
		}
		if filter.AppID != "" && job.AppID != filter.AppID {
			continue
		}
		if filter.Status != "" && string(job.Status) != filter.Status {
			continue
		}
		if filter.Target != "" && job.Target != filter.Target {
			continue
		}

		jobs = append(jobs, job)
	}

	return jobs, nil
}

func (m *MemoryStore) ListAllEvents(_ context.Context, filter EventListFilter) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	scoped := make([]Event, 0)
	for _, jobID := range m.order {
		job := m.jobs[jobID]
		if filter.TenantID != "" && job.TenantID != filter.TenantID {
			continue
		}
		if filter.AppID != "" && job.AppID != filter.AppID {
			continue
		}
		scoped = append(scoped, m.events[jobID]...)
	}
	sort.SliceStable(scoped, func(i, j int) bool {
		if scoped[i].CreatedAt.Equal(scoped[j].CreatedAt) {
			return scoped[i].ID < scoped[j].ID
		}
		return scoped[i].CreatedAt.Before(scoped[j].CreatedAt)
	})

	events := make([]Event, 0, limit)
	afterCursor := filter.AfterEventID == ""
	for _, event := range scoped {
		if !afterCursor {
			if event.ID == filter.AfterEventID {
				afterCursor = true
			}
			continue
		}
		events = append(events, event)
		if len(events) >= limit {
			return events, nil
		}
	}
	return events, nil
}

func (m *MemoryStore) ListEvents(_ context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.listEventsLocked(jobID, afterSequence, limit)
}

func (m *MemoryStore) WaitEvents(ctx context.Context, jobID string, afterSequence int, limit int) ([]Event, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	events, found, err := m.listEventsLocked(jobID, afterSequence, limit)
	if err != nil || !found || len(events) > 0 {
		return events, found, err
	}

	wake := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.cond.Broadcast()
			m.mu.Unlock()
		case <-wake:
		}
	}()
	defer close(wake)

	for {
		m.cond.Wait()
		if ctx.Err() != nil {
			return nil, true, ctx.Err()
		}
		events, found, err = m.listEventsLocked(jobID, afterSequence, limit)
		if err != nil || !found || len(events) > 0 {
			return events, found, err
		}
	}
}

func (m *MemoryStore) listEventsLocked(jobID string, afterSequence int, limit int) ([]Event, bool, error) {
	if _, ok := m.jobs[jobID]; !ok {
		return nil, false, nil
	}

	if limit <= 0 {
		limit = 100
	}
	items := m.events[jobID]
	events := make([]Event, 0, len(items))
	for _, event := range items {
		if event.Sequence <= afterSequence {
			continue
		}
		events = append(events, event)
		if len(events) >= limit {
			break
		}
	}

	return events, true, nil
}

func (m *MemoryStore) UpdateStatus(_ context.Context, id string, status Status) (Job, bool, error) {
	if !KnownStatus(status) {
		return Job{}, false, fmt.Errorf("unknown job status %q", status)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[id]
	if !ok {
		return Job{}, false, nil
	}
	if job.Status == status {
		return job, true, nil
	}
	if TerminalStatus(job.Status) {
		return job, true, nil
	}

	job.Status = status
	job.UpdatedAt = m.now().UTC()
	m.jobs[id] = job
	m.appendEventLocked(job, string(status), map[string]any{
		"status": string(status),
		"target": job.Target,
	})

	return job, true, nil
}

func (m *MemoryStore) ApplyWorkerEvent(_ context.Context, event WorkerEvent) (Job, bool, error) {
	if event.JobID == "" {
		return Job{}, false, fmt.Errorf("worker event job_id is required")
	}
	if event.Type == "" {
		return Job{}, false, fmt.Errorf("worker event type is required")
	}
	if !knownWorkerEventType(event.Type) {
		return Job{}, false, fmt.Errorf("worker event type %q is not supported", event.Type)
	}
	if event.APIVersion == "" {
		return Job{}, false, fmt.Errorf("worker event api_version is required")
	}
	if event.TraceID == "" {
		return Job{}, false, fmt.Errorf("worker event trace_id is required")
	}
	if workerEventKey(event) == "" {
		return Job{}, false, fmt.Errorf("worker event must include event_id or positive sequence")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[event.JobID]
	if !ok {
		return Job{}, false, nil
	}
	if event.APIVersion != job.APIVersion {
		return job, true, fmt.Errorf("worker event api_version %q does not match job api_version %q", event.APIVersion, job.APIVersion)
	}
	if job.TraceID != "" && event.TraceID != job.TraceID {
		return job, true, fmt.Errorf("worker event trace_id %q does not match job trace_id %q", event.TraceID, job.TraceID)
	}

	eventKey := workerEventKey(event)
	if eventKey != "" {
		keys := m.eventKey[job.ID]
		if keys == nil {
			keys = make(map[string]struct{})
			m.eventKey[job.ID] = keys
		}
		if _, seen := keys[eventKey]; seen {
			return job, true, nil
		}
		keys[eventKey] = struct{}{}
	}

	if TerminalStatus(job.Status) {
		return job, true, nil
	}

	if err := validateWorkerEventData(event.Type, event.Data); err != nil {
		return job, true, err
	}
	data, _ := sanitizeWorkerData(event.Type, event.Data).(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	metadata := map[string]any{
		"event_id": event.EventID,
		"sequence": event.Sequence,
		"type":     event.Type,
	}
	if !event.CreatedAt.IsZero() {
		metadata["created_at"] = event.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	data["worker_event"] = metadata

	nextStatus := statusFromWorkerEvent(event, job.Status)
	if result := resultFromWorkerEvent(event, data); result != nil {
		job.Result = result
	}
	if shouldAdvanceStatus(job.Status, nextStatus) {
		job.Status = nextStatus
	}
	job.UpdatedAt = m.now().UTC()
	m.jobs[job.ID] = job
	m.appendEventLocked(job, event.Type, data)

	return job, true, nil
}

func (m *MemoryStore) Ready(context.Context) error {
	return nil
}

func (m *MemoryStore) CountsByStatus(_ context.Context, filter ListFilter) (map[Status]int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	counts := map[Status]int{}
	total := 0
	for _, id := range m.order {
		job := m.jobs[id]
		if filter.TenantID != "" && job.TenantID != filter.TenantID {
			continue
		}
		if filter.AppID != "" && job.AppID != filter.AppID {
			continue
		}
		if filter.Status != "" && string(job.Status) != filter.Status {
			continue
		}
		if filter.Target != "" && job.Target != filter.Target {
			continue
		}
		counts[job.Status]++
		total++
	}
	return counts, total, nil
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (m *MemoryStore) appendEventLocked(job Job, eventType string, data map[string]any) {
	m.eventSeqGlobal++
	m.eventSeq[job.ID]++
	sequence := m.eventSeq[job.ID]
	m.events[job.ID] = append(m.events[job.ID], Event{
		ID:         fmt.Sprintf("evt_%012d", m.eventSeqGlobal),
		JobID:      job.ID,
		APIVersion: job.APIVersion,
		Type:       eventType,
		Sequence:   sequence,
		Data:       data,
		TraceID:    job.TraceID,
		CreatedAt:  m.now().UTC(),
	})
	m.cond.Broadcast()
}

func workerEventKey(event WorkerEvent) string {
	if event.EventID != "" {
		return "id:" + event.EventID
	}
	if event.Sequence > 0 {
		return fmt.Sprintf("seq:%d:%s", event.Sequence, event.Type)
	}
	return ""
}

func statusFromWorkerEvent(event WorkerEvent, fallback Status) Status {
	if status, ok := event.Data["status"].(string); ok && KnownStatus(Status(status)) {
		return Status(status)
	}
	switch event.Type {
	case "created":
		return StatusCreated
	case "queued":
		return StatusQueued
	case "assigned":
		return StatusAssigned
	case "running":
		return StatusRunning
	case "token", "token_streaming":
		return StatusTokenStreaming
	case "completing":
		return StatusCompleting
	case "completed":
		return StatusCompleted
	case "completed_with_warnings":
		return StatusCompletedWithWarnings
	case "failed", "failed_retryable":
		if retryable, ok := event.Data["retryable"].(bool); ok && !retryable {
			return StatusFailedTerminal
		}
		return StatusFailedRetryable
	case "failed_terminal":
		return StatusFailedTerminal
	case "blocked":
		return StatusFailedRetryable
	case "dead_letter":
		return StatusDeadLetter
	case "cancelled", "canceled":
		return StatusCanceled
	case "timed_out", "timeout":
		return StatusTimedOut
	default:
		return fallback
	}
}

func shouldAdvanceStatus(current Status, next Status) bool {
	if current == next || !KnownStatus(next) || TerminalStatus(current) {
		return false
	}
	return statusRank(next) >= statusRank(current)
}

func statusRank(status Status) int {
	switch status {
	case StatusQueued:
		return 10
	case StatusAssigned:
		return 20
	case StatusRunning:
		return 30
	case StatusTokenStreaming:
		return 40
	case StatusCompleting:
		return 50
	case StatusCompleted, StatusCompletedWithWarnings, StatusFailedRetryable, StatusFailedTerminal, StatusDeadLetter, StatusCanceled, StatusTimedOut:
		return 100
	default:
		return 0
	}
}

func resultFromWorkerEvent(event WorkerEvent, data map[string]any) any {
	status := statusFromWorkerEvent(event, "")
	if status != StatusCompleted && status != StatusCompletedWithWarnings && event.Type != "completed" && event.Type != "completed_with_warnings" {
		return nil
	}
	raw, ok := data["result"]
	if !ok {
		return nil
	}
	result, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	if _, ok := result["output"].(map[string]any); ok {
		return result
	}
	text, ok := result["text"].(string)
	if !ok {
		return result
	}
	return map[string]any{
		"output": map[string]any{
			"text":       text,
			"plain_text": text,
			"sections":   map[string]any{},
		},
		"validation": map[string]any{
			"schema_id": "worker.text.out",
			"passed":    true,
		},
		"cached":       false,
		"cache_source": nil,
	}
}

func knownWorkerEventType(eventType string) bool {
	switch eventType {
	case "created",
		"queued",
		"assigned",
		"running",
		"browser_opened",
		"session.manual_action_required",
		"prompt_submitted",
		"token",
		"token_streaming",
		"completing",
		"completed",
		"completed_with_warnings",
		"failed",
		"failed_retryable",
		"failed_terminal",
		"dead_letter",
		"cancelled",
		"canceled",
		"timed_out",
		"timeout",
		"artifact_created",
		"blocked",
		"warning":
		return true
	default:
		return false
	}
}

func validateWorkerEventData(eventType string, data map[string]any) error {
	if data == nil {
		return nil
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if len(encoded) > 64*1024 {
		return fmt.Errorf("worker event data exceeds 65536 bytes")
	}
	return payloadpolicy.Validate(removeUnsafeWorkerKeys(eventType, data))
}

func sanitizeWorkerData(eventType string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		output := make(map[string]any, len(typed))
		for key, child := range typed {
			if redactWorkerEventKey(eventType, key, child) {
				output[key] = "[redacted]"
				continue
			}
			output[key] = sanitizeWorkerData(eventType, child)
		}
		return output
	case []any:
		output := make([]any, len(typed))
		for index, child := range typed {
			output[index] = sanitizeWorkerData(eventType, child)
		}
		return output
	default:
		return value
	}
}

func removeUnsafeWorkerKeys(eventType string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		output := make(map[string]any, len(typed))
		for key, child := range typed {
			if redactWorkerEventKey(eventType, key, child) || allowManualRuntimeEventKey(eventType, key, child) {
				continue
			}
			output[key] = removeUnsafeWorkerKeys(eventType, child)
		}
		return output
	case []any:
		output := make([]any, len(typed))
		for index, child := range typed {
			output[index] = removeUnsafeWorkerKeys(eventType, child)
		}
		return output
	default:
		return value
	}
}

func redactWorkerEventKey(eventType string, key string, value any) bool {
	if allowManualRuntimeEventKey(eventType, key, value) {
		return false
	}
	if eventType == "session.manual_action_required" && (payloadpolicy.NormalizeKey(key) == "session_id" || payloadpolicy.NormalizeKey(key) == "novnc_url") {
		return true
	}
	switch payloadpolicy.NormalizeKey(key) {
	case "access_token",
		"api_key",
		"apikey",
		"auth_token",
		"authorization",
		"bearer",
		"captcha_response",
		"captcha_solution",
		"captcha_token",
		"cookie",
		"cookies",
		"credential",
		"credentials",
		"id_token",
		"mfa_code",
		"novnc_url",
		"password",
		"private_key",
		"refresh_token",
		"secret",
		"session",
		"session_cookie",
		"session_id",
		"session_state",
		"set_cookie",
		"storage_state",
		"totp",
		"x_api_key":
		return true
	default:
		return false
	}
}

func allowManualRuntimeEventKey(eventType string, key string, value any) bool {
	if eventType != "session.manual_action_required" {
		return false
	}
	switch payloadpolicy.NormalizeKey(key) {
	case "session_id":
		return isSafeRuntimeSessionID(value)
	case "novnc_url":
		return isSafeLoopbackNoVNCURL(value)
	default:
		return false
	}
}

func isSafeRuntimeSessionID(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 128 {
		return false
	}
	for _, char := range text {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func isSafeLoopbackNoVNCURL(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(text))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	if host == "" || parsed.Port() == "" || !strings.HasPrefix(parsed.EscapedPath(), "/session/") {
		return false
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}
