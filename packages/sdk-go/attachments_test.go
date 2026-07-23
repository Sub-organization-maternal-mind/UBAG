package ubag

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func attachmentRequest() JSON {
	return JSON{
		"client": JSON{},
		"job": JSON{
			"target":       "mock",
			"command_type": "chat.prompt",
			"input":        JSON{"prompt": "inspect"},
		},
	}
}

func TestCreateJobWithAttachmentsUploadsEveryFile(t *testing.T) {
	var mu sync.Mutex
	var uploaded []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/jobs" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"job_id":"job_1"}`)
			return
		}
		mu.Lock()
		uploaded = append(uploaded, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateJobWithAttachments(context.Background(), attachmentRequest(), []AttachmentUpload{
		{Key: "a.txt", ContentType: "text/plain", Kind: "document", Body: []byte("a")},
		{Key: "b.wav", ContentType: "audio/wav", Kind: "voice", Body: []byte("b")},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(uploaded) != 2 ||
		!strings.Contains(strings.Join(uploaded, "|"), "/a.txt") ||
		!strings.Contains(strings.Join(uploaded, "|"), "/b.wav") {
		t.Fatalf("uploads = %#v", uploaded)
	}
}

func TestCreateJobMultipartPreservesOrderMetadataAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != "idem-attachments" {
			t.Errorf("idempotency key = %q", got)
		}
		if got := r.Header.Get("Ubag-Api-Version"); got != DefaultAPIVersion {
			t.Errorf("api version = %q", got)
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content type = %q, err=%v", mediaType, err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		var names []string
		var filenames []string
		var contentTypes []string
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, part.FormName())
			filenames = append(filenames, part.FileName())
			contentTypes = append(contentTypes, part.Header.Get("Content-Type"))
			_, _ = io.Copy(io.Discard, part)
		}
		if strings.Join(names, ",") != "job,report.pdf,voice.webm" {
			t.Errorf("part names = %#v", names)
		}
		if filenames[1] != "clinical-report.pdf" || filenames[2] != "note.webm" {
			t.Errorf("filenames = %#v", filenames)
		}
		if contentTypes[1] != "application/pdf" || contentTypes[2] != "audio/webm" {
			t.Errorf("content types = %#v", contentTypes)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(JSON{"job_id": "job_2"})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateJobMultipart(
		context.Background(),
		attachmentRequest(),
		[]AttachmentUpload{
			{Key: "report.pdf", Filename: "clinical-report.pdf", ContentType: "application/pdf", Kind: "document", Body: []byte("pdf")},
			{Key: "voice.webm", Filename: "note.webm", ContentType: "audio/webm", Kind: "voice", Body: []byte("webm")},
		},
		WithIdempotencyKey("idem-attachments"),
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAttachmentLimits(t *testing.T) {
	if AttachmentMaxFileBytes != 32*1024*1024 || AttachmentMaxManifestFiles != 32 {
		t.Fatalf("limits = %d bytes, %d files", AttachmentMaxFileBytes, AttachmentMaxManifestFiles)
	}
}
