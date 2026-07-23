package ubag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sync"
	"time"
)

// AttachmentUpload is one file to attach to a job: metadata plus its bytes. The
// bytes ride to the artifact store; only the metadata is declared inline in
// job.input.attachments.
type AttachmentUpload struct {
	Key         string
	Filename    string
	ContentType string
	Kind        string
	Body        []byte
}

// attachmentManifest builds the job.input.attachments manifest (metadata only).
func attachmentManifest(attachments []AttachmentUpload) []map[string]any {
	manifest := make([]map[string]any, 0, len(attachments))
	for _, attachment := range attachments {
		entry := map[string]any{
			"key":          attachment.Key,
			"content_type": attachment.ContentType,
			"kind":         attachment.Kind,
		}
		if attachment.Filename != "" {
			entry["filename"] = attachment.Filename
		}
		manifest = append(manifest, entry)
	}
	return manifest
}

// injectAttachments sets job.input.attachments on a (cloned) request body.
func injectAttachments(body JSON, attachments []AttachmentUpload) error {
	job, ok := body["job"].(map[string]any)
	if !ok {
		return fmt.Errorf("request.job must be an object")
	}
	input, ok := job["input"].(map[string]any)
	if !ok {
		input = map[string]any{}
	}
	input["attachments"] = attachmentManifest(attachments)
	job["input"] = input
	body["job"] = job
	return nil
}

// SubmitJobWithAttachments creates a job via the key-reference flow: submit the
// job (held until its files arrive), then upload every attachment's bytes to the
// artifact store in parallel. The returned response is the held job; the gateway
// dispatches it automatically once the final upload lands.
func (client *Client) SubmitJobWithAttachments(ctx context.Context, request JSON, attachments []AttachmentUpload, options ...RequestOption) (JSON, error) {
	body, err := cloneJSON(request)
	if err != nil {
		return nil, err
	}
	if err := injectAttachments(body, attachments); err != nil {
		return nil, err
	}
	created, err := client.CreateJob(ctx, body, options...)
	if err != nil {
		return nil, err
	}
	jobID := stringValue(created["job_id"])
	if jobID == "" {
		return created, fmt.Errorf("create job returned no job_id")
	}

	var wg sync.WaitGroup
	errs := make([]error, len(attachments))
	for i := range attachments {
		wg.Add(1)
		go func(index int, attachment AttachmentUpload) {
			defer wg.Done()
			if _, err := client.PutJobArtifact(ctx, jobID, attachment.Key, attachment.Body, attachment.ContentType); err != nil {
				errs[index] = err
			}
		}(i, attachments[i])
	}
	wg.Wait()
	for _, uploadErr := range errs {
		if uploadErr != nil {
			return created, uploadErr
		}
	}
	return created, nil
}

// CreateJobMultipart creates a job with attachments in a single
// multipart/form-data request: the job envelope is the first part, followed by
// one file part per attachment (part name == key). The job is born complete and
// dispatches immediately.
func (client *Client) CreateJobMultipart(ctx context.Context, request JSON, attachments []AttachmentUpload, options ...RequestOption) (JSON, error) {
	config := client.resolveOptions(options...)
	body, err := cloneJSON(request)
	if err != nil {
		return nil, err
	}
	apiVersion := stringValue(body["api_version"])
	if apiVersion == "" {
		apiVersion = config.apiVersion
	}
	idempotencyKey := stringValue(body["idempotency_key"])
	if idempotencyKey == "" {
		idempotencyKey = config.idempotencyKey
	}
	if idempotencyKey == "" {
		idempotencyKey = GenerateIdempotencyKey(time.Now())
	}
	body["api_version"] = apiVersion
	body["idempotency_key"] = idempotencyKey
	ensureSDKMetadata(body)
	if err := injectAttachments(body, attachments); err != nil {
		return nil, err
	}
	envelopeBytes, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	jobPart, err := writer.CreateFormField("job")
	if err != nil {
		return nil, err
	}
	if _, err := jobPart.Write(envelopeBytes); err != nil {
		return nil, err
	}
	for _, attachment := range attachments {
		filename := attachment.Filename
		if filename == "" {
			filename = attachment.Key
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, attachment.Key, filename))
		if attachment.ContentType != "" {
			header.Set("Content-Type", attachment.ContentType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(attachment.Body); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	config.apiVersion = apiVersion
	config.idempotencyKey = idempotencyKey
	responseBody, _, err := client.requestBytes(ctx, http.MethodPost, "/v1/jobs", buf.Bytes(), writer.FormDataContentType(), config)
	if err != nil {
		return nil, err
	}
	var payload JSON
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}
