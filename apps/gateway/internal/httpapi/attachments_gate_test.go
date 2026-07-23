package httpapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
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
	fileField, _ := mw.CreateFormFile("report.pdf", "report.pdf")
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
