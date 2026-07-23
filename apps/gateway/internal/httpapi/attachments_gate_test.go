package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/outbox"
	"github.com/ubag/ubag/apps/gateway/internal/templates"
)

// A key-reference attachment job is held (status created, not enqueued) until its
// declared artifact key is uploaded, then dispatches exactly once.
func TestAttachmentGateHoldsUntilUploaded(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Executor: dispatcher}).Handler()

	body := `{"api_version":"2026-05-22","idempotency_key":"idem_attach_gate_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"chat.prompt","input":{"prompt":"summarize","attachments":[{"key":"report.pdf","content_type":"application/pdf","kind":"document"}]}}}`
	create := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_attach_gate_0001"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want 202; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Status != "created" {
		t.Fatalf("held job status = %q, want created", created.Status)
	}
	if len(dispatcher.enqueued) != 0 {
		t.Fatalf("held job must not be enqueued yet, got %d", len(dispatcher.enqueued))
	}

	put := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/report.pdf", "%PDF-1.7 fake", "application/pdf", authHeaders("idem_attach_gate_put_0001"))
	if put.Code != http.StatusCreated {
		t.Fatalf("put status = %d, want 201; body=%s", put.Code, put.Body.String())
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("job should dispatch once its only attachment lands, got %d", len(dispatcher.enqueued))
	}

	got := doJSON(server, http.MethodGet, "/v1/jobs/"+created.JobID, "", authHeaders(""))
	var fetched jobResponse
	_ = json.Unmarshal(got.Body.Bytes(), &fetched)
	if fetched.Status != "queued" {
		t.Fatalf("dispatched job status = %q, want queued", fetched.Status)
	}
}

// A job with two attachments stays held after the first upload and dispatches
// only when the second (final) key lands.
func TestAttachmentGateWaitsForAllFiles(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Executor: dispatcher}).Handler()

	body := `{"api_version":"2026-05-22","idempotency_key":"idem_attach_multi_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"chat.prompt","input":{"prompt":"compare","attachments":[{"key":"a.pdf","content_type":"application/pdf","kind":"document"},{"key":"b.webm","content_type":"audio/webm","kind":"voice"}]}}}`
	create := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_attach_multi_0001"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	_ = json.Unmarshal(create.Body.Bytes(), &created)

	put1 := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/a.pdf", "%PDF fake", "application/pdf", authHeaders("idem_multi_put_a"))
	if put1.Code != http.StatusCreated {
		t.Fatalf("put a status = %d; body=%s", put1.Code, put1.Body.String())
	}
	if len(dispatcher.enqueued) != 0 {
		t.Fatalf("job must stay held until all files land, got %d", len(dispatcher.enqueued))
	}

	put2 := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/b.webm", "fake-opus", "audio/webm", authHeaders("idem_multi_put_b"))
	if put2.Code != http.StatusCreated {
		t.Fatalf("put b status = %d; body=%s", put2.Code, put2.Body.String())
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("job should dispatch after the final file, got %d", len(dispatcher.enqueued))
	}
}

// A target that declares no attachments policy rejects attachments at create.
func TestAttachmentsUnsupportedTargetRejected(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_attach_unsupported_1","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"submit","input":{"attachments":[{"key":"x.pdf","content_type":"application/pdf","kind":"document"}]}}}`
	resp := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_attach_unsupported_1"))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", resp.Code, resp.Body.String())
	}
	var env errorEnvelope
	_ = json.Unmarshal(resp.Body.Bytes(), &env)
	if env.Error.Code != "UBAG-VALIDATION-ATTACHMENTS-UNSUPPORTED-001" {
		t.Fatalf("error code = %q, want UBAG-VALIDATION-ATTACHMENTS-UNSUPPORTED-001", env.Error.Code)
	}
}

// A content type the target does not accept is rejected at create.
func TestAttachmentsContentTypeRejected(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_attach_ct_reject_1","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"chat.prompt","input":{"attachments":[{"key":"x.exe","content_type":"application/x-msdownload","kind":"document"}]}}}`
	resp := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_attach_ct_reject_1"))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", resp.Code, resp.Body.String())
	}
	var env errorEnvelope
	_ = json.Unmarshal(resp.Body.Bytes(), &env)
	if env.Error.Code != "UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001" {
		t.Fatalf("error code = %q, want UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001", env.Error.Code)
	}
}

// A multipart one-shot create stores its file parts and dispatches immediately.
func TestMultipartOneShotDispatchesImmediately(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Executor: dispatcher}).Handler()

	jobJSON := `{"api_version":"2026-05-22","idempotency_key":"idem_multipart_oneshot_1","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"chat.prompt","input":{"prompt":"summarize","attachments":[{"key":"report.pdf","content_type":"application/pdf","kind":"document"}]}}}`

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	jobField, _ := mw.CreateFormField("job")
	_, _ = jobField.Write([]byte(jobJSON))
	fileHeader := make(textproto.MIMEHeader)
	fileHeader.Set("Content-Disposition", `form-data; name="report.pdf"; filename="report.pdf"`)
	fileHeader.Set("Content-Type", "application/pdf")
	fileField, _ := mw.CreatePart(fileHeader)
	_, _ = fileField.Write([]byte("%PDF-1.7 fake bytes"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer dev-secret")
	req.Header.Set("Ubag-Api-Version", DefaultAPIVersion)
	req.Header.Set("Idempotency-Key", "idem_multipart_oneshot_1")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("multipart create status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("multipart one-shot should dispatch immediately, got %d", len(dispatcher.enqueued))
	}
}

// A multipart part whose field name matches no declared attachment key is rejected.
func TestMultipartUnknownPartRejected(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	jobJSON := `{"api_version":"2026-05-22","idempotency_key":"idem_multipart_unknown_1","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"chat.prompt","input":{"prompt":"x","attachments":[{"key":"report.pdf","content_type":"application/pdf","kind":"document"}]}}}`

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	jobField, _ := mw.CreateFormField("job")
	_, _ = jobField.Write([]byte(jobJSON))
	stray, _ := mw.CreateFormFile("stray.pdf", "stray.pdf")
	_, _ = stray.Write([]byte("nope"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer dev-secret")
	req.Header.Set("Ubag-Api-Version", DefaultAPIVersion)
	req.Header.Set("Idempotency-Key", "idem_multipart_unknown_1")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var env errorEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "UBAG-VALIDATION-MULTIPART-PART-UNKNOWN-001" {
		t.Fatalf("error code = %q, want UBAG-VALIDATION-MULTIPART-PART-UNKNOWN-001", env.Error.Code)
	}
}

func TestLegacyAudioAliasCreateAndUploadMIMEGate(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Executor: dispatcher}).Handler()
	body := `{"api_version":"2026-05-22","idempotency_key":"idem_legacy_audio_0001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"chat.prompt","input":{"prompt":"transcribe","audio_artifact_key":"note.webm"}}}`
	create := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders("idem_legacy_audio_0001"))
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want 202; body=%s", create.Code, create.Body.String())
	}
	var created jobResponse
	_ = json.Unmarshal(create.Body.Bytes(), &created)
	if created.Status != "created" {
		t.Fatalf("legacy audio job status = %q, want created", created.Status)
	}

	bad := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/note.webm", "not audio", "application/pdf", authHeaders("idem_legacy_audio_bad_1"))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("non-audio upload status = %d, want 400; body=%s", bad.Code, bad.Body.String())
	}
	if len(dispatcher.enqueued) != 0 {
		t.Fatal("legacy audio job dispatched after non-audio upload")
	}
	good := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/note.webm", "audio", "audio/webm", authHeaders("idem_legacy_audio_good"))
	if good.Code != http.StatusCreated {
		t.Fatalf("audio upload status = %d, want 201; body=%s", good.Code, good.Body.String())
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("legacy audio job dispatch count = %d, want 1", len(dispatcher.enqueued))
	}
}

func TestHeldAttachmentPUTFailsClosed(t *testing.T) {
	t.Run("undeclared key", func(t *testing.T) {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
		created := createHeldPDFJob(t, server, "idem_put_unknown_create")
		resp := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/other.pdf", "pdf", "application/pdf", authHeaders("idem_put_unknown_file"))
		assertErrorCode(t, resp, http.StatusBadRequest, "UBAG-VALIDATION-MULTIPART-PART-UNKNOWN-001")
	})
	t.Run("manifest MIME mismatch", func(t *testing.T) {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
		created := createHeldPDFJob(t, server, "idem_put_mime_create_1")
		resp := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/report.pdf", "text", "text/plain", authHeaders("idem_put_mime_file_1"))
		assertErrorCode(t, resp, http.StatusBadRequest, "UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001")
	})
	t.Run("adapter file-size policy", func(t *testing.T) {
		restore := setAttachmentPolicyForTest("chatgpt_web", attachmentPolicy{
			declared: true, maxFiles: 10, maxFileBytes: 4,
			accepted: map[string][]string{"document": {"application/pdf"}},
		})
		defer restore()
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
		created := createHeldPDFJob(t, server, "idem_put_size_create_1")
		resp := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/report.pdf", "12345", "application/pdf", authHeaders("idem_put_size_file_01"))
		assertErrorCode(t, resp, http.StatusRequestEntityTooLarge, "UBAG-VALIDATION-BODY-TOO-LARGE-001")
	})
}

func TestMultipartRejectsMIMEAndDuplicateParts(t *testing.T) {
	jobJSON := validMultipartJob("idem_multipart_validation_1")
	t.Run("MIME mismatch", func(t *testing.T) {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
		resp := doMultipart(t, server, jobJSON, "idem_multipart_validation_1", []multipartTestPart{
			{key: "report.pdf", contentType: "text/plain", body: "not pdf"},
		})
		assertErrorCode(t, resp, http.StatusBadRequest, "UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001")
	})
	t.Run("duplicate key", func(t *testing.T) {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
		resp := doMultipart(t, server, jobJSON, "idem_multipart_validation_1", []multipartTestPart{
			{key: "report.pdf", contentType: "application/pdf", body: "one"},
			{key: "report.pdf", contentType: "application/pdf", body: "two"},
		})
		assertErrorCode(t, resp, http.StatusBadRequest, "UBAG-VALIDATION-MULTIPART-PART-DUPLICATE-001")
	})
}

func TestMultipartPreflightsBeforeStreamingBinaryParts(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	jobJSON := strings.Replace(validMultipartJob("idem_multipart_preflight_1"), `"kind":"document"`, `"kind":"archive"`, 1)
	body, contentType := multipartBytes(t, jobJSON, []multipartTestPart{
		{key: "report.pdf", contentType: "application/pdf", body: strings.Repeat("x", 32<<10)},
	})
	marker := bytes.Index(body, []byte(strings.Repeat("x", 1024)))
	if marker < 0 {
		t.Fatal("binary marker not found")
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", &failAfterReader{data: body, failAt: marker + 8192})
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer dev-secret")
	req.Header.Set("Ubag-Api-Version", DefaultAPIVersion)
	req.Header.Set("Idempotency-Key", "idem_multipart_preflight_1")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	assertErrorCode(t, rec, http.StatusBadRequest, "UBAG-VALIDATION-ATTACHMENT-KIND-001")
}

func TestMultipartIdempotencyIncludesAttachmentBytes(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	jobJSON := validMultipartJob("idem_multipart_bytes_hash")
	first := doMultipart(t, server, jobJSON, "idem_multipart_bytes_hash", []multipartTestPart{
		{key: "report.pdf", contentType: "application/pdf", body: "version one"},
	})
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d; body=%s", first.Code, first.Body.String())
	}
	replay := doMultipart(t, server, jobJSON, "idem_multipart_bytes_hash", []multipartTestPart{
		{key: "report.pdf", contentType: "application/pdf", body: "version one"},
	})
	if replay.Code != http.StatusAccepted {
		t.Fatalf("same-byte replay status = %d, want 202; body=%s", replay.Code, replay.Body.String())
	}
	conflict := doMultipart(t, server, jobJSON, "idem_multipart_bytes_hash", []multipartTestPart{
		{key: "report.pdf", contentType: "application/pdf", body: "version two"},
	})
	assertErrorCode(t, conflict, http.StatusConflict, "UBAG-VALIDATION-IDEMPOTENCY-CONFLICT-001")
}

func TestMultipartEnforcesPolicyTotalAndPerFileCaps(t *testing.T) {
	restore := setAttachmentPolicyForTest("chatgpt_web", attachmentPolicy{
		declared: true, maxFiles: 2, maxFileBytes: 4,
		accepted: map[string][]string{"document": {"application/pdf"}},
	})
	defer restore()
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	jobJSON := strings.Replace(validMultipartJob("idem_multipart_caps_001"),
		`{"key":"report.pdf","content_type":"application/pdf","kind":"document"}`,
		`{"key":"a.pdf","content_type":"application/pdf","kind":"document"},{"key":"b.pdf","content_type":"application/pdf","kind":"document"}`, 1)
	resp := doMultipart(t, server, jobJSON, "idem_multipart_caps_001", []multipartTestPart{
		{key: "a.pdf", contentType: "application/pdf", body: "1234"},
		{key: "b.pdf", contentType: "application/pdf", body: "56789"},
	})
	assertErrorCode(t, resp, http.StatusRequestEntityTooLarge, "UBAG-VALIDATION-BODY-TOO-LARGE-001")
}

func TestBatchAttachmentEntryUsesHeldDispatchGate(t *testing.T) {
	dispatcher := &recordingExecutor{}
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Executor: dispatcher}).Handler()
	body := `{"api_version":"2026-05-22","jobs":[
		{"idempotency_key":"idem_batch_text_00001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"mock","command_type":"chat.prompt","input":{"prompt":"hello"}}},
		{"idempotency_key":"idem_batch_attach_001","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"chat.prompt","input":{"prompt":"summarize","attachments":[{"key":"report.pdf","content_type":"application/pdf","kind":"document"}]}}}
	]}`
	resp := doJSON(server, http.MethodPost, "/v1/jobs/batch", body, authHeaders(""))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("batch status = %d, want 202; body=%s", resp.Code, resp.Body.String())
	}
	var batch batchCreateJobResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Accepted != 2 || batch.Rejected != 0 || len(batch.Results) != 2 {
		t.Fatalf("unexpected batch outcome: %#v", batch)
	}
	if len(dispatcher.enqueued) != 1 {
		t.Fatalf("only text entry should enqueue initially, got %d", len(dispatcher.enqueued))
	}
	attachmentJobID := batch.Results[1].JobID
	get := doJSON(server, http.MethodGet, "/v1/jobs/"+attachmentJobID, "", authHeaders(""))
	var held jobResponse
	_ = json.Unmarshal(get.Body.Bytes(), &held)
	if held.Status != "created" {
		t.Fatalf("batch attachment status = %q, want created", held.Status)
	}
	put := doRaw(server, http.MethodPut, "/v1/jobs/"+attachmentJobID+"/artifacts/report.pdf", "pdf", "application/pdf", authHeaders("idem_batch_attach_put"))
	if put.Code != http.StatusCreated {
		t.Fatalf("upload status = %d; body=%s", put.Code, put.Body.String())
	}
	if len(dispatcher.enqueued) != 2 {
		t.Fatalf("batch attachment should dispatch after upload, got %d total enqueues", len(dispatcher.enqueued))
	}
}

func TestAttachmentMetricsExposePlannedDimensions(t *testing.T) {
	srv := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"})
	server := srv.Handler()
	created := createHeldPDFJob(t, server, "idem_metrics_attach_create")
	put := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/report.pdf", "pdf", "application/pdf", authHeaders("idem_metrics_attach_put_1"))
	if put.Code != http.StatusCreated {
		t.Fatalf("put status = %d; body=%s", put.Code, put.Body.String())
	}
	metrics := doJSON(server, http.MethodGet, "/v1/metrics", "", nil)
	for _, want := range []string{
		`ubag_attachments_total{kind="document",outcome="stored"} 1`,
		`ubag_jobs_awaiting_attachments 0`,
		`ubag_job_attachment_gate_timeouts_total 0`,
		`ubag_attachment_materialize_failures_total{reason="artifact_read"} 0`,
		`ubag_multipart_jobs_total{outcome="accepted"} 0`,
		`ubag_multipart_rollbacks_total 0`,
	} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, metrics.Body.String())
		}
	}
}

func TestMultipartSharedPreflightBeforeBinaryStaging(t *testing.T) {
	t.Run("authorization", func(t *testing.T) {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "viewer"}).Handler()
		jobJSON := validMultipartJob("idem_preflight_auth_001")
		rec, reader := doMultipartWithFailingBinary(t, server, jobJSON, "idem_preflight_auth_001")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
		if reader.offset >= reader.failAt {
			t.Fatalf("authorization consumed binary stream: offset=%d failAt=%d", reader.offset, reader.failAt)
		}
	})
	t.Run("template mutated manifest", func(t *testing.T) {
		templateStore := templates.NewMemoryStore(templates.Template{
			ID: "bad.attach.v1", TenantID: "*", AppID: "*",
			Target: "chatgpt_web", CommandType: "chat.prompt",
			InputDefaults: map[string]any{"attachments": []any{
				map[string]any{"key": "report.pdf", "content_type": "application/pdf", "kind": "archive"},
			}},
		})
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Templates: templateStore}).Handler()
		jobJSON := `{"api_version":"2026-05-22","idempotency_key":"idem_preflight_template","client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"template_id":"bad.attach.v1","input":{"prompt":"x"}}}`
		rec, reader := doMultipartWithFailingBinary(t, server, jobJSON, "idem_preflight_template")
		assertErrorCode(t, rec, http.StatusBadRequest, "UBAG-VALIDATION-ATTACHMENT-KIND-001")
		if reader.offset >= reader.failAt {
			t.Fatalf("template validation consumed binary stream: offset=%d failAt=%d", reader.offset, reader.failAt)
		}
	})
}

func TestChunkedMultipartUsesPolicyDerivedStreamCap(t *testing.T) {
	restore := setAttachmentPolicyForTest("chatgpt_web", attachmentPolicy{
		declared: true, maxFiles: 1, maxFileBytes: 4,
		accepted: map[string][]string{"document": {"application/pdf"}},
	})
	defer restore()
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", MaxBodyBytes: 4096}).Handler()
	jobJSON := validMultipartJob("idem_chunked_policy_cap")
	body, contentType := multipartBytes(t, jobJSON, []multipartTestPart{
		{key: "report.pdf", contentType: "application/pdf", body: strings.Repeat("x", 256<<10)},
	})
	reader := &countingReader{data: body}
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", reader)
	req.ContentLength = -1
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer dev-secret")
	req.Header.Set("Ubag-Api-Version", DefaultAPIVersion)
	req.Header.Set("Idempotency-Key", "idem_chunked_policy_cap")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	if reader.offset > len(jobJSON)+(64<<10)+4096 {
		t.Fatalf("chunked parser consumed %d bytes beyond policy/framing bound", reader.offset)
	}
}

func TestMultipartStoredMetricWaitsForFullCommit(t *testing.T) {
	store := &failNthPutArtifactStore{ArtifactStore: artifacts.NewMemoryArtifactStore(), failAt: 2}
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Artifacts: store}).Handler()
	jobJSON := strings.Replace(validMultipartJob("idem_metric_rollback_01"),
		`{"key":"report.pdf","content_type":"application/pdf","kind":"document"}`,
		`{"key":"a.pdf","content_type":"application/pdf","kind":"document"},{"key":"b.pdf","content_type":"application/pdf","kind":"document"}`, 1)
	resp := doMultipart(t, server, jobJSON, "idem_metric_rollback_01", []multipartTestPart{
		{key: "a.pdf", contentType: "application/pdf", body: "one"},
		{key: "b.pdf", contentType: "application/pdf", body: "two"},
	})
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", resp.Code, resp.Body.String())
	}
	metrics := doJSON(server, http.MethodGet, "/v1/metrics", "", nil)
	if !strings.Contains(metrics.Body.String(), `ubag_attachments_total{kind="document",outcome="stored"} 0`) {
		t.Fatalf("rolled-back attachment did not leave stored metric at zero:\n%s", metrics.Body.String())
	}
	if !strings.Contains(metrics.Body.String(), `ubag_multipart_rollbacks_total 1`) {
		t.Fatalf("rollback metric missing:\n%s", metrics.Body.String())
	}
}

func TestDeclaredAttachmentBytesBecomeImmutableAfterDispatch(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer"}).Handler()
	created := createHeldPDFJob(t, server, "idem_immutable_create_1")
	first := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/report.pdf", "original", "application/pdf", authHeaders("idem_immutable_first_01"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first put status = %d; body=%s", first.Code, first.Body.String())
	}
	replay := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/report.pdf", "original", "application/pdf", authHeaders("idem_immutable_first_01"))
	if replay.Code != http.StatusCreated {
		t.Fatalf("idempotent replay status = %d, want 201; body=%s", replay.Code, replay.Body.String())
	}
	overwrite := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/report.pdf", "changed", "application/pdf", authHeaders("idem_immutable_overwrite"))
	assertErrorCode(t, overwrite, http.StatusConflict, "UBAG-VALIDATION-ATTACHMENT-IMMUTABLE-001")
	deleteResp := doRaw(server, http.MethodDelete, "/v1/jobs/"+created.JobID+"/artifacts/report.pdf", "", "", authHeaders("idem_immutable_delete_1"))
	assertErrorCode(t, deleteResp, http.StatusConflict, "UBAG-VALIDATION-ATTACHMENT-IMMUTABLE-001")
}

func TestAttachmentFinalizeReportsListFailure(t *testing.T) {
	store := &listFailingArtifactStore{ArtifactStore: artifacts.NewMemoryArtifactStore()}
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Artifacts: store}).Handler()
	created := createHeldPDFJob(t, server, "idem_finalize_list_create")
	store.failList = true
	resp := doRaw(server, http.MethodPut, "/v1/jobs/"+created.JobID+"/artifacts/report.pdf", "pdf", "application/pdf", authHeaders("idem_finalize_list_put1"))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("put status = %d, want 500; body=%s", resp.Code, resp.Body.String())
	}
}

func TestRecoverQueuedAttachmentOutbox(t *testing.T) {
	outboxStore := outbox.NewMemoryStore()
	srv := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Outbox: outboxStore})
	job, err := srv.jobs.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: DefaultAPIVersion, TenantID: "tenant_default", AppID: "app_default",
		Target: "chatgpt_web", CommandType: "chat.prompt", AwaitingAttachments: true,
		Input: map[string]any{"attachments": []any{
			map[string]any{"key": "report.pdf", "content_type": "application/pdf", "kind": "document"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := srv.jobs.TransitionStatus(context.Background(), job.ID, jobstore.StatusCreated, jobstore.StatusQueued); err != nil || !changed {
		t.Fatalf("seed queued status changed=%v err=%v", changed, err)
	}
	srv.recoverQueuedAttachmentOutbox(context.Background())
	pending, err := outboxStore.Pending(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != job.ID || pending[0].Topic != "jobs.dispatch" {
		t.Fatalf("recovered outbox events = %#v", pending)
	}
}

type listFailingArtifactStore struct {
	artifacts.ArtifactStore
	failList bool
}

func (s *listFailingArtifactStore) ListArtifacts(ctx context.Context, jobID string) ([]artifacts.ArtifactRecord, error) {
	if s.failList {
		return nil, fmt.Errorf("forced list failure")
	}
	return s.ArtifactStore.ListArtifacts(ctx, jobID)
}

func createHeldPDFJob(t *testing.T, server http.Handler, idem string) jobResponse {
	t.Helper()
	body := fmt.Sprintf(`{"api_version":"2026-05-22","idempotency_key":%q,"client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"chat.prompt","input":{"attachments":[{"key":"report.pdf","content_type":"application/pdf","kind":"document"}]}}}`, idem)
	resp := doJSON(server, http.MethodPost, "/v1/jobs", body, authHeaders(idem))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("create status = %d; body=%s", resp.Code, resp.Body.String())
	}
	var created jobResponse
	_ = json.Unmarshal(resp.Body.Bytes(), &created)
	return created
}

func validMultipartJob(idem string) string {
	return fmt.Sprintf(`{"api_version":"2026-05-22","idempotency_key":%q,"client":{"app_id":"test","app_version":"0.0.0","sdk":{"name":"test","version":"0.0.0"}},"job":{"target":"chatgpt_web","command_type":"chat.prompt","input":{"attachments":[{"key":"report.pdf","content_type":"application/pdf","kind":"document"}]}}}`, idem)
}

type multipartTestPart struct {
	key         string
	contentType string
	body        string
}

func multipartBytes(t *testing.T, jobJSON string, parts []multipartTestPart) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	jobField, err := mw.CreateFormField("job")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = jobField.Write([]byte(jobJSON))
	for _, part := range parts {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, part.key, part.key))
		header.Set("Content-Type", part.contentType)
		field, err := mw.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = field.Write([]byte(part.body))
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

func doMultipart(t *testing.T, server http.Handler, jobJSON, idem string, parts []multipartTestPart) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartBytes(t, jobJSON, parts)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer dev-secret")
	req.Header.Set("Ubag-Api-Version", DefaultAPIVersion)
	req.Header.Set("Idempotency-Key", idem)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error: %v; body=%s", err, rec.Body.String())
	}
	if env.Error.Code != code {
		t.Fatalf("error code = %q, want %q; body=%s", env.Error.Code, code, rec.Body.String())
	}
}

func setAttachmentPolicyForTest(target string, policy attachmentPolicy) func() {
	attachmentPolicyMu.Lock()
	old, existed := attachmentPolicyCache[target]
	attachmentPolicyCache[target] = policy
	attachmentPolicyMu.Unlock()
	return func() {
		attachmentPolicyMu.Lock()
		defer attachmentPolicyMu.Unlock()
		if existed {
			attachmentPolicyCache[target] = old
		} else {
			delete(attachmentPolicyCache, target)
		}
	}
}

type failAfterReader struct {
	data   []byte
	offset int
	failAt int
}

func doMultipartWithFailingBinary(t *testing.T, server http.Handler, jobJSON, idem string) (*httptest.ResponseRecorder, *failAfterReader) {
	t.Helper()
	body, contentType := multipartBytes(t, jobJSON, []multipartTestPart{
		{key: "report.pdf", contentType: "application/pdf", body: strings.Repeat("x", 32<<10)},
	})
	marker := bytes.Index(body, []byte(strings.Repeat("x", 1024)))
	reader := &failAfterReader{data: body, failAt: marker + 8192}
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", reader)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer dev-secret")
	req.Header.Set("Ubag-Api-Version", DefaultAPIVersion)
	req.Header.Set("Idempotency-Key", idem)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec, reader
}

type countingReader struct {
	data   []byte
	offset int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

type failNthPutArtifactStore struct {
	artifacts.ArtifactStore
	puts   int
	failAt int
}

func (s *failNthPutArtifactStore) PutArtifact(ctx context.Context, jobID, key, contentType string, body io.Reader, size int64) (artifacts.ArtifactRecord, error) {
	s.puts++
	if s.puts == s.failAt {
		return artifacts.ArtifactRecord{}, fmt.Errorf("forced put failure")
	}
	return s.ArtifactStore.PutArtifact(ctx, jobID, key, contentType, body, size)
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if r.offset >= r.failAt {
		return 0, io.ErrUnexpectedEOF
	}
	remaining := r.failAt - r.offset
	if len(p) > remaining {
		p = p[:remaining]
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}
