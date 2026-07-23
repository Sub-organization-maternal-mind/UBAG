package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/attachments"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/webhooks"
)

const (
	// defaultMaxAttachments caps attachment count when an adapter's attachments
	// policy does not set max_files.
	defaultMaxAttachments = 10
	// maxAttachmentsHardLimit is the absolute per-job attachment ceiling, matching
	// the job-request schema's attachments maxItems. Used as a parse-time guard in
	// the multipart handler before the adapter policy is known.
	maxAttachmentsHardLimit = 32
	// attachmentUploadTTL bounds how long a job may sit held (StatusCreated)
	// waiting for its attachment bytes before the sweeper fails it and frees its
	// concurrency token.
	attachmentUploadTTL = 10 * time.Minute
)

// attachmentPolicy is the gateway's view of an adapter's manifest attachments
// block. An absent block (declared=false) means the target accepts no
// attachments and any attachment is rejected (fail-closed, like model_catalog).
type attachmentPolicy struct {
	declared     bool
	maxFiles     int
	maxFileBytes int64
	accepted     map[string][]string // kind -> content-type patterns (exact or "family/*")
}

type labeledMetric struct {
	key   string
	count int64
}

type labeledCounter struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (c *labeledCounter) add(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = make(map[string]int64)
	}
	c.counts[key]++
}

func (c *labeledCounter) snapshotWithDefaults(defaults []string) []labeledMetric {
	c.mu.Lock()
	defer c.mu.Unlock()
	merged := make(map[string]int64, len(c.counts)+len(defaults))
	for _, key := range defaults {
		merged[key] = 0
	}
	for key, count := range c.counts {
		merged[key] = count
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]labeledMetric, 0, len(keys))
	for _, key := range keys {
		out = append(out, labeledMetric{key: key, count: merged[key]})
	}
	return out
}

// accepts reports whether a content type is allowed for the given kind.
func (p attachmentPolicy) accepts(kind, contentType string) bool {
	patterns, ok := p.accepted[strings.TrimSpace(kind)]
	if !ok {
		return false
	}
	for _, pattern := range patterns {
		if contentTypeMatches(pattern, contentType) {
			return true
		}
	}
	return false
}

// effectiveMaxFiles resolves the per-job attachment count for this policy.
func (p attachmentPolicy) effectiveMaxFiles() int {
	if p.maxFiles > 0 {
		return p.maxFiles
	}
	return defaultMaxAttachments
}

func (p attachmentPolicy) effectiveMaxFileBytes() int64 {
	if p.maxFileBytes > 0 && p.maxFileBytes < maxArtifactBodyBytes {
		return p.maxFileBytes
	}
	return maxArtifactBodyBytes
}

func (p attachmentPolicy) totalBytesCap() int64 {
	return int64(p.effectiveMaxFiles()) * p.effectiveMaxFileBytes()
}

func contentTypeMatches(pattern, contentType string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if pattern == "" || contentType == "" {
		return false
	}
	if pattern == contentType {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(contentType, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

var (
	attachmentPolicyMu    sync.Mutex
	attachmentPolicyCache = map[string]attachmentPolicy{}
)

// resolveAttachmentPolicy loads and caches the target adapter's attachments
// policy from its manifest, mirroring resolveModelCatalog.
func resolveAttachmentPolicy(target string) attachmentPolicy {
	target = strings.TrimSpace(target)
	attachmentPolicyMu.Lock()
	defer attachmentPolicyMu.Unlock()
	if p, ok := attachmentPolicyCache[target]; ok {
		return p
	}
	p := loadAttachmentPolicyFromDisk(target)
	attachmentPolicyCache[target] = p
	return p
}

func loadAttachmentPolicyFromDisk(target string) attachmentPolicy {
	if !isTargetKey(target) {
		return attachmentPolicy{}
	}
	dir := adaptersDir()
	if dir == "" {
		return attachmentPolicy{}
	}
	raw, err := os.ReadFile(filepath.Join(dir, target, "manifest.json"))
	if err != nil {
		return attachmentPolicy{}
	}
	// Adapter manifests are authored on Windows and carry a UTF-8 BOM that
	// encoding/json rejects; strip it before unmarshalling.
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	var manifest struct {
		Attachments *struct {
			MaxFiles     int   `json:"max_files"`
			MaxFileBytes int64 `json:"max_file_bytes"`
			Accepted     []struct {
				Kind         string   `json:"kind"`
				ContentTypes []string `json:"content_types"`
			} `json:"accepted"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.Attachments == nil {
		return attachmentPolicy{}
	}
	policy := attachmentPolicy{
		declared:     true,
		maxFiles:     manifest.Attachments.MaxFiles,
		maxFileBytes: manifest.Attachments.MaxFileBytes,
		accepted:     make(map[string][]string, len(manifest.Attachments.Accepted)),
	}
	for _, entry := range manifest.Attachments.Accepted {
		kind := strings.TrimSpace(entry.Kind)
		if kind == "" {
			continue
		}
		policy.accepted[kind] = append(policy.accepted[kind], entry.ContentTypes...)
	}
	return policy
}

// validateAttachmentsForCreate checks a job's declared attachments against the
// target adapter's policy at create time, mirroring validateModelSettingsForCreate.
// It returns ("", "", true) when the job declares no attachments.
func validateAttachmentsForCreate(target string, input map[string]any) (code, message string, ok bool) {
	declared, err := attachments.DeclaredAttachments(input)
	if err != nil {
		return attachments.ErrorCode(err), err.Error(), false
	}
	if len(declared) == 0 {
		return "", "", true
	}
	policy := resolveAttachmentPolicy(target)
	if !policy.declared {
		return "UBAG-VALIDATION-ATTACHMENTS-UNSUPPORTED-001", fmt.Sprintf("target %q does not accept attachments", strings.TrimSpace(target)), false
	}
	if max := policy.effectiveMaxFiles(); len(declared) > max {
		return "UBAG-VALIDATION-ATTACHMENTS-COUNT-001", fmt.Sprintf("job declares %d attachments; target allows at most %d", len(declared), max), false
	}
	for _, att := range declared {
		if att.LegacyAudio {
			continue
		}
		if !policy.accepts(att.Kind, att.ContentType) {
			return "UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001", fmt.Sprintf("attachment content_type %q is not accepted for kind %q by this target", att.ContentType, att.Kind), false
		}
	}
	return "", "", true
}

// jobDeclaresAttachments reports whether a job input carries any attachments
// (including the folded audio_artifact_key alias).
func jobDeclaresAttachments(input map[string]any) bool {
	declared, err := attachments.DeclaredAttachments(input)
	return err == nil && len(declared) > 0
}

func (s *Server) awaitingAttachmentJobCount(ctx context.Context) int {
	held, err := s.jobs.List(ctx, jobstore.ListFilter{Status: string(jobstore.StatusCreated)})
	if err != nil {
		return 0
	}
	count := 0
	for _, job := range held {
		if jobDeclaresAttachments(job.Input) {
			count++
		}
	}
	return count
}

// dispatchHeldJob resolves the dispatch region and enqueues a job that was
// created in the held StatusCreated state. It is the enqueue half of the
// attachment dispatch gate, used by the PUT completion hook and the multipart
// one-shot. It returns an error rather than writing an HTTP response — the gate
// callers have already responded to their own request.
func (s *Server) dispatchHeldJob(ctx context.Context, job jobstore.Job) error {
	dispatchCtx := ctx
	if s.regionRouter != nil {
		targetRegion, err := s.regionRouter.Route(dispatchCtx, job.TenantID)
		if err != nil {
			return fmt.Errorf("region route: %w", err)
		}
		if targetRegion != "" {
			dispatchCtx = executor.WithDispatchRegion(dispatchCtx, string(targetRegion))
		}
	}
	if s.outbox != nil {
		env := executor.EnvelopeFromJobWithConversation(dispatchCtx, job, s.conversations)
		envelopeBytes, err := json.Marshal(env)
		if err != nil {
			return fmt.Errorf("marshal envelope: %w", err)
		}
		if err := s.outbox.Append(ctx, job.ID, "jobs.dispatch", envelopeBytes); err != nil {
			return fmt.Errorf("outbox append: %w", err)
		}
		return nil
	}
	if _, err := s.executor.EnqueueJob(dispatchCtx, job); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

// maybeDispatchAfterArtifact enqueues a held (StatusCreated) attachment job once
// every declared artifact key is present. It is invoked after each successful
// artifact PUT (including idempotent replays) and by the multipart one-shot after
// it stores all parts. The CAS TransitionStatus guarantees exactly one caller
// wins and dispatches, so concurrent final PUTs (and the PUT-vs-sweeper race) are
// safe. `job` is the job as loaded before this PUT stored its artifact; the CAS,
// not the stale status, is authoritative.
func (s *Server) maybeDispatchAfterArtifact(ctx context.Context, job jobstore.Job) error {
	s.attachmentMutationMu.Lock()
	defer s.attachmentMutationMu.Unlock()
	return s.maybeDispatchAfterArtifactLocked(ctx, job)
}

func (s *Server) maybeDispatchAfterArtifactLocked(ctx context.Context, job jobstore.Job) (resultErr error) {
	if job.Status != jobstore.StatusCreated {
		return nil
	}
	declared, err := attachments.DeclaredAttachments(job.Input)
	if err != nil || len(declared) == 0 {
		return err
	}
	records, err := s.artifactSt.ListArtifacts(ctx, job.ID)
	if err != nil {
		s.attachmentDispatchFailures.Add(1)
		return fmt.Errorf("list attachment artifacts: %w", err)
	}
	present := make(map[string]struct{}, len(records))
	for _, rec := range records {
		present[rec.Key] = struct{}{}
	}
	for _, att := range declared {
		if _, ok := present[att.Key]; !ok {
			return nil // not complete yet
		}
	}

	updated, changed, err := s.jobs.TransitionStatus(ctx, job.ID, jobstore.StatusCreated, jobstore.StatusQueued)
	if err != nil || !changed {
		resultErr = err
		return // lost the race, or already advanced — the winner dispatches
	}
	if err := s.dispatchHeldJob(ctx, updated); err != nil {
		// The job already flipped to queued but could not be enqueued: fail it
		// retryable so the client/webhook sees it and the token is freed.
		_, _, _ = s.jobs.TransitionStatus(ctx, job.ID, jobstore.StatusQueued, jobstore.StatusFailedRetryable)
		s.releaseConcurrencyTokenForJob(job.ID)
		s.attachmentDispatchFailures.Add(1)
		slog.Error("attachment job dispatch failed", "job_id", job.ID, "error", err)
		return err
	}
	return nil
}

// stagedAttachment is one multipart file part streamed to a temp file, awaiting
// storage in the artifact store once its owning job exists.
type stagedAttachment struct {
	key         string
	kind        string
	contentType string
	path        string
	size        int64
	sha256      string
}

type stagedAttachmentsKey struct{}

func stagedAttachmentsFromContext(ctx context.Context) []stagedAttachment {
	if v, ok := ctx.Value(stagedAttachmentsKey{}).([]stagedAttachment); ok {
		return v
	}
	return nil
}

type multipartHashKey struct{}

func multipartHashFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(multipartHashKey{}).(string); ok {
		return value
	}
	return ""
}

// storeStagedAttachments writes each staged multipart file to the artifact store
// under its declared key. On any failure it rolls back: best-effort delete of the
// artifacts already stored, fail the job terminally, release its token and the
// idempotency reservation, and write a 5xx. Returns false when it has already
// responded with an error.
func (s *Server) storeStagedAttachments(w http.ResponseWriter, r *http.Request, job jobstore.Job, staged []stagedAttachment, scope idempotency.Scope) bool {
	stored := make([]string, 0, len(staged))
	rollback := func() {
		for _, key := range stored {
			_ = s.artifactSt.DeleteArtifact(r.Context(), job.ID, key)
		}
		_, _, _ = s.jobs.TransitionStatus(r.Context(), job.ID, jobstore.StatusCreated, jobstore.StatusFailedTerminal)
		_ = s.idempotency.Release(r.Context(), scope)
		s.releaseConcurrencyTokenForJob(job.ID)
		s.multipartRollbacks.Add(1)
	}
	for _, st := range staged {
		file, err := os.Open(st.path)
		if err != nil {
			rollback()
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to read staged attachment"))
			return false
		}
		_, err = s.artifactSt.PutArtifact(r.Context(), job.ID, st.key, st.contentType, file, st.size)
		_ = file.Close()
		if err != nil {
			rollback()
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to store attachment"))
			return false
		}
		stored = append(stored, st.key)
		s.artifactCaptures.Add(1)
		s.attachmentsStored.Add(1)
		s.attachmentOutcomes.add(st.kind + "|stored")
	}
	return true
}

// createJobMultipart handles a multipart/form-data POST /v1/jobs: the first part
// is the job JSON envelope, followed by one binary file part per declared
// attachment whose field name equals the attachment key. It streams each file to
// a bounded temp file, cross-checks the parts against the declared manifest, then
// delegates to createJob (reusing all validation, idempotency, and the gate) with
// the staged files carried in the request context so the job is born complete.
func (s *Server) createJobMultipart(w http.ResponseWriter, r *http.Request) {
	outcome := "rejected"
	defer func() { s.multipartOutcomes.add(outcome) }()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	boundary := params["boundary"]
	if err != nil || boundary == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MULTIPART-001", "multipart/form-data boundary is missing"))
		return
	}
	// Bound the transfer at the broad schema/gateway ceiling until the job part
	// identifies the adapter policy. The tighter policy-derived cap is applied
	// immediately after that envelope is preflighted.
	parseCap := int64(maxAttachmentsHardLimit)*maxArtifactBodyBytes + s.maxBody
	reader := multipart.NewReader(http.MaxBytesReader(w, r.Body, parseCap), boundary)

	// The first part must be the job JSON envelope.
	part, err := reader.NextPart()
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MULTIPART-001", "multipart body is empty or malformed"))
		return
	}
	if part.FormName() != "job" {
		_ = part.Close()
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MULTIPART-PART-ORDER-001", "the first multipart part must be the job JSON envelope"))
		return
	}
	jobJSON, err := io.ReadAll(io.LimitReader(part, s.maxBody))
	_ = part.Close()
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-READ-001", "failed to read the job envelope part"))
		return
	}

	request, declared, policy, code, message := s.preflightMultipartEnvelope(r, jobJSON)
	if code != "" {
		s.writeError(w, r, http.StatusBadRequest, validationError(code, message))
		return
	}
	totalCap := policy.totalBytesCap()
	if r.ContentLength > 0 && r.ContentLength > totalCap+s.maxBody {
		s.writeError(w, r, http.StatusRequestEntityTooLarge, validationError("UBAG-VALIDATION-BODY-TOO-LARGE-001", "multipart body exceeds the target attachment total-size limit"))
		return
	}
	declaredByKey := make(map[string]attachments.Attachment, len(declared))
	for _, att := range declared {
		declaredByKey[att.Key] = att
	}

	staged := make([]stagedAttachment, 0)
	cleanup := func() {
		for _, st := range staged {
			_ = os.Remove(st.path)
		}
	}
	defer cleanup()

	stagedKeys := make(map[string]struct{}, len(declared))
	var totalWritten int64
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MULTIPART-001", "multipart body is malformed"))
			return
		}
		key := part.FormName()
		if !attachments.ValidKey(key) {
			_ = part.Close()
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-ATTACHMENT-KEY-001", "attachment part name must be a single non-empty path segment"))
			return
		}
		if _, duplicate := stagedKeys[key]; duplicate {
			_ = part.Close()
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MULTIPART-PART-DUPLICATE-001", fmt.Sprintf("multipart part %q appears more than once", key)))
			return
		}
		att, declaredKey := declaredByKey[key]
		if !declaredKey {
			_ = part.Close()
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MULTIPART-PART-UNKNOWN-001", fmt.Sprintf("multipart part %q does not match any declared attachment key", key)))
			return
		}
		contentType := safeArtifactContentType(part.Header.Get("Content-Type"))
		if !attachmentUploadMIMEAccepted(policy, att, contentType) {
			_ = part.Close()
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001", fmt.Sprintf("multipart part %q content type %q does not match its declaration or target policy", key, contentType)))
			return
		}
		tmp, err := os.CreateTemp("", "ubag-mp-*"+filepath.Ext(key))
		if err != nil {
			_ = part.Close()
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to stage attachment"))
			return
		}
		hasher := sha256.New()
		fileCap := policy.effectiveMaxFileBytes()
		// Per-file cap: read one byte past the limit to detect oversize while
		// hashing exactly the bytes staged for the artifact store.
		written, copyErr := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(part, fileCap+1))
		_ = tmp.Close()
		_ = part.Close()
		if copyErr != nil {
			_ = os.Remove(tmp.Name())
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-READ-001", "failed to read an attachment part"))
			return
		}
		if written > fileCap {
			_ = os.Remove(tmp.Name())
			s.writeError(w, r, http.StatusRequestEntityTooLarge, validationError("UBAG-VALIDATION-BODY-TOO-LARGE-001", "an attachment part exceeds the target per-file limit"))
			return
		}
		totalWritten += written
		if totalWritten > totalCap {
			_ = os.Remove(tmp.Name())
			s.writeError(w, r, http.StatusRequestEntityTooLarge, validationError("UBAG-VALIDATION-BODY-TOO-LARGE-001", "multipart attachments exceed the target total-size limit"))
			return
		}
		stagedKeys[key] = struct{}{}
		staged = append(staged, stagedAttachment{
			key: key, kind: att.Kind, contentType: contentType, path: tmp.Name(), size: written,
			sha256: fmt.Sprintf("%x", hasher.Sum(nil)),
		})
	}

	for _, att := range declared {
		if _, ok := stagedKeys[att.Key]; !ok {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MULTIPART-PART-MISSING-001", fmt.Sprintf("declared attachment %q has no multipart file part", att.Key)))
			return
		}
	}

	// Replay the envelope as a JSON body and hand the staged files to createJob.
	r.Body = io.NopCloser(strings.NewReader(string(jobJSON)))
	r.ContentLength = int64(len(jobJSON))
	r.Header.Set("Content-Type", "application/json")
	_ = request // request was intentionally decoded and validated before staging.
	ctx := context.WithValue(r.Context(), stagedAttachmentsKey{}, staged)
	ctx = context.WithValue(ctx, multipartHashKey{}, canonicalMultipartAttachmentsHash(staged))
	recorder := &multipartStatusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.createJob(recorder, r.WithContext(ctx))
	if recorder.status >= 200 && recorder.status < 300 {
		outcome = "accepted"
	}
}

type multipartStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *multipartStatusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (s *Server) preflightMultipartEnvelope(r *http.Request, jobJSON []byte) (createJobRequest, []attachments.Attachment, attachmentPolicy, string, string) {
	var request createJobRequest
	if err := json.Unmarshal(jobJSON, &request); err != nil {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-JSON-001", "job envelope part is not valid JSON"
	}
	if !isTargetKey(request.Job.Target) {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-JOB-TARGET-001", "job.target is required and invalid"
	}
	if !isTargetKey(request.Job.CommandType) {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-JOB-COMMAND-001", "job.command_type is required and invalid"
	}
	if request.Job.Input == nil {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-JOB-INPUT-001", "job.input is required and must be an object"
	}
	if strings.TrimSpace(request.Client.AppID) == "" || strings.TrimSpace(request.Client.AppVersion) == "" ||
		strings.TrimSpace(request.Client.SDK.Name) == "" || strings.TrimSpace(request.Client.SDK.Version) == "" {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-CLIENT-SDK-001", "client app and SDK identity fields are required"
	}
	headerKey := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	bodyKey := strings.TrimSpace(request.IdempotencyKey)
	if headerKey != "" && bodyKey != "" && headerKey != bodyKey {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-IDEMPOTENCY-KEY-MISMATCH-001", "idempotency_key must match Idempotency-Key"
	}
	if key := firstNonEmpty(headerKey, bodyKey); !isIdempotencyKey(key) {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-IDEMPOTENCY-KEY-001", "a valid Idempotency-Key is required"
	}
	if err := validateExecutableJobPayload(request); err != nil {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-JOB-PAYLOAD-SAFETY-001", err.Error()
	}
	if _, _, err := webhooks.CallbackFromMap(request.Job.Callbacks, s.webhookURLs); err != nil {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-WEBHOOK-CALLBACK-001", err.Error()
	}
	if code, message, ok := validateModelSettingsForCreate(request.Job.Target, request.Job.ModelSettings); !ok {
		return request, nil, attachmentPolicy{}, code, message
	}
	if code, message, ok := validateAttachmentsForCreate(request.Job.Target, request.Job.Input); !ok {
		return request, nil, attachmentPolicy{}, code, message
	}
	declared, err := attachments.DeclaredAttachments(request.Job.Input)
	if err != nil {
		return request, nil, attachmentPolicy{}, attachments.ErrorCode(err), err.Error()
	}
	if len(declared) == 0 {
		return request, nil, attachmentPolicy{}, "UBAG-VALIDATION-MULTIPART-PART-MISSING-001", "multipart job must declare at least one attachment"
	}
	return request, declared, resolveAttachmentPolicy(request.Job.Target), "", ""
}

func attachmentUploadMIMEAccepted(policy attachmentPolicy, att attachments.Attachment, actual string) bool {
	if att.LegacyAudio {
		return policy.accepts("audio", actual)
	}
	return actual == att.ContentType && policy.accepts(att.Kind, actual)
}

func canonicalMultipartAttachmentsHash(staged []stagedAttachment) string {
	type hashTuple struct {
		Key         string `json:"key"`
		ContentType string `json:"content_type"`
		SHA256      string `json:"sha256"`
	}
	tuples := make([]hashTuple, 0, len(staged))
	for _, item := range staged {
		tuples = append(tuples, hashTuple{Key: item.key, ContentType: item.contentType, SHA256: item.sha256})
	}
	sort.Slice(tuples, func(i, j int) bool {
		if tuples[i].Key != tuples[j].Key {
			return tuples[i].Key < tuples[j].Key
		}
		if tuples[i].ContentType != tuples[j].ContentType {
			return tuples[i].ContentType < tuples[j].ContentType
		}
		return tuples[i].SHA256 < tuples[j].SHA256
	})
	encoded, _ := json.Marshal(tuples)
	return string(encoded)
}

// sweepStuckAttachmentJobs fails jobs that have sat in the held StatusCreated
// state past attachmentUploadTTL without all their attachments arriving. It uses
// the same CAS as the completion hook, so it can never race a job into a double
// state: whichever of {last PUT, sweeper} wins the CAS decides.
func (s *Server) sweepStuckAttachmentJobs(ctx context.Context) {
	held, err := s.jobs.List(ctx, jobstore.ListFilter{Status: string(jobstore.StatusCreated)})
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-attachmentUploadTTL)
	for _, job := range held {
		if job.CreatedAt.After(cutoff) {
			continue
		}
		s.attachmentMutationMu.Lock()
		_, changed, err := s.jobs.TransitionStatus(ctx, job.ID, jobstore.StatusCreated, jobstore.StatusFailedTerminal)
		if err != nil || !changed {
			s.attachmentMutationMu.Unlock()
			continue
		}
		s.releaseConcurrencyTokenForJob(job.ID)
		s.attachmentGateTimeouts.Add(1)
		// Best-effort cleanup of any partial uploads; artifact GC reclaims the rest.
		if records, lerr := s.artifactSt.ListArtifacts(ctx, job.ID); lerr == nil {
			for _, rec := range records {
				_ = s.artifactSt.DeleteArtifact(ctx, job.ID, rec.Key)
			}
		}
		s.attachmentMutationMu.Unlock()
		slog.Warn("failed attachment job past upload TTL", "job_id", job.ID)
	}
}

// recoverQueuedAttachmentOutbox repairs the narrow crash window between the
// held-job CAS (created -> queued) and durable outbox append. Outbox append is
// idempotent by job ID, so re-appending every queued attachment job is safe.
// Direct executors are intentionally excluded because enqueue is not generally
// idempotent.
func (s *Server) recoverQueuedAttachmentOutbox(ctx context.Context) {
	if s.outbox == nil {
		return
	}
	queued, err := s.jobs.List(ctx, jobstore.ListFilter{Status: string(jobstore.StatusQueued)})
	if err != nil {
		slog.Error("list queued attachment jobs for outbox recovery", "error", err)
		return
	}
	for _, job := range queued {
		if !jobDeclaresAttachments(job.Input) {
			continue
		}
		if err := s.dispatchHeldJob(ctx, job); err != nil {
			s.attachmentDispatchFailures.Add(1)
			slog.Error("recover queued attachment outbox", "job_id", job.ID, "error", err)
		}
	}
}

// RunAttachmentSweeper runs sweepStuckAttachmentJobs on a ticker until ctx is
// done. Wire it in serve.go alongside the other background loops.
func (s *Server) RunAttachmentSweeper(ctx context.Context) {
	s.recoverQueuedAttachmentOutbox(ctx)
	ticker := time.NewTicker(attachmentUploadTTL / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepStuckAttachmentJobs(ctx)
			s.recoverQueuedAttachmentOutbox(ctx)
		}
	}
}
