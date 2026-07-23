package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/attachments"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
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
		return "UBAG-VALIDATION-ATTACHMENTS-SHAPE-001", err.Error(), false
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
		if !attachments.ValidKey(att.Key) {
			return "UBAG-VALIDATION-ATTACHMENT-KEY-001", "attachment key must be a single non-empty path segment", false
		}
		if !attachments.ValidKind(att.Kind) {
			return "UBAG-VALIDATION-ATTACHMENT-KIND-001", "attachment kind must be one of document|image|audio|video|voice", false
		}
		if att.ContentType == "" || !policy.accepts(att.Kind, att.ContentType) {
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
func (s *Server) maybeDispatchAfterArtifact(ctx context.Context, job jobstore.Job) {
	if job.Status != jobstore.StatusCreated {
		return
	}
	declared, err := attachments.DeclaredAttachments(job.Input)
	if err != nil || len(declared) == 0 {
		return
	}
	records, err := s.artifactSt.ListArtifacts(ctx, job.ID)
	if err != nil {
		return
	}
	present := make(map[string]struct{}, len(records))
	for _, rec := range records {
		present[rec.Key] = struct{}{}
	}
	for _, att := range declared {
		if _, ok := present[att.Key]; !ok {
			return // not complete yet
		}
	}

	updated, changed, err := s.jobs.TransitionStatus(ctx, job.ID, jobstore.StatusCreated, jobstore.StatusQueued)
	if err != nil || !changed {
		return // lost the race, or already advanced — the winner dispatches
	}
	if err := s.dispatchHeldJob(ctx, updated); err != nil {
		// The job already flipped to queued but could not be enqueued: fail it
		// retryable so the client/webhook sees it and the token is freed.
		_, _, _ = s.jobs.TransitionStatus(ctx, job.ID, jobstore.StatusQueued, jobstore.StatusFailedRetryable)
		s.releaseConcurrencyTokenForJob(job.ID)
		s.attachmentDispatchFailures.Add(1)
		slog.Error("attachment job dispatch failed", "job_id", job.ID, "error", err)
	}
}

// stagedAttachment is one multipart file part streamed to a temp file, awaiting
// storage in the artifact store once its owning job exists.
type stagedAttachment struct {
	key         string
	contentType string
	path        string
	size        int64
}

type stagedAttachmentsKey struct{}

func stagedAttachmentsFromContext(ctx context.Context) []stagedAttachment {
	if v, ok := ctx.Value(stagedAttachmentsKey{}).([]stagedAttachment); ok {
		return v
	}
	return nil
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
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	boundary := params["boundary"]
	if err != nil || boundary == "" {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MULTIPART-001", "multipart/form-data boundary is missing"))
		return
	}
	// Bound the whole transfer before buffering anything.
	totalCap := int64(maxAttachmentsHardLimit)*maxArtifactBodyBytes + s.maxBody
	reader := multipart.NewReader(http.MaxBytesReader(w, r.Body, totalCap), boundary)

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

	staged := make([]stagedAttachment, 0)
	cleanup := func() {
		for _, st := range staged {
			_ = os.Remove(st.path)
		}
	}
	defer cleanup()

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
		if len(staged) >= maxAttachmentsHardLimit {
			_ = part.Close()
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-ATTACHMENTS-COUNT-001", fmt.Sprintf("too many attachment parts; at most %d are allowed", maxAttachmentsHardLimit)))
			return
		}
		contentType := safeArtifactContentType(part.Header.Get("Content-Type"))
		tmp, err := os.CreateTemp("", "ubag-mp-*"+filepath.Ext(key))
		if err != nil {
			_ = part.Close()
			s.writeError(w, r, http.StatusInternalServerError, internalError("failed to stage attachment"))
			return
		}
		// Per-file cap: read one byte past the limit to detect oversize.
		written, copyErr := io.Copy(tmp, io.LimitReader(part, maxArtifactBodyBytes+1))
		_ = tmp.Close()
		_ = part.Close()
		if copyErr != nil {
			_ = os.Remove(tmp.Name())
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-BODY-READ-001", "failed to read an attachment part"))
			return
		}
		if written > maxArtifactBodyBytes {
			_ = os.Remove(tmp.Name())
			s.writeError(w, r, http.StatusRequestEntityTooLarge, validationError("UBAG-VALIDATION-BODY-TOO-LARGE-001", "an attachment part exceeds the 32 MiB per-file limit"))
			return
		}
		staged = append(staged, stagedAttachment{key: key, contentType: contentType, path: tmp.Name(), size: written})
	}

	// Cross-check the staged parts against the declared manifest so the two paths
	// agree on the key set. Full validation runs inside createJob.
	var envelope struct {
		Job struct {
			Input map[string]any `json:"input"`
		} `json:"job"`
	}
	if err := json.Unmarshal(jobJSON, &envelope); err != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-JSON-001", "job envelope part is not valid JSON"))
		return
	}
	declared, derr := attachments.DeclaredAttachments(envelope.Job.Input)
	if derr != nil {
		s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-ATTACHMENTS-SHAPE-001", derr.Error()))
		return
	}
	declaredKeys := make(map[string]struct{}, len(declared))
	for _, att := range declared {
		declaredKeys[att.Key] = struct{}{}
	}
	stagedKeys := make(map[string]struct{}, len(staged))
	for _, st := range staged {
		if _, ok := declaredKeys[st.key]; !ok {
			s.writeError(w, r, http.StatusBadRequest, validationError("UBAG-VALIDATION-MULTIPART-PART-UNKNOWN-001", fmt.Sprintf("multipart part %q does not match any declared attachment key", st.key)))
			return
		}
		stagedKeys[st.key] = struct{}{}
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
	s.multipartJobs.Add(1)
	ctx := context.WithValue(r.Context(), stagedAttachmentsKey{}, staged)
	s.createJob(w, r.WithContext(ctx))
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
		_, changed, err := s.jobs.TransitionStatus(ctx, job.ID, jobstore.StatusCreated, jobstore.StatusFailedTerminal)
		if err != nil || !changed {
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
		slog.Warn("failed attachment job past upload TTL", "job_id", job.ID)
	}
}

// RunAttachmentSweeper runs sweepStuckAttachmentJobs on a ticker until ctx is
// done. Wire it in serve.go alongside the other background loops.
func (s *Server) RunAttachmentSweeper(ctx context.Context) {
	ticker := time.NewTicker(attachmentUploadTTL / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepStuckAttachmentJobs(ctx)
		}
	}
}
