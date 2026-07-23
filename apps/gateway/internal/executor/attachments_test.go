package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
)

// A job carrying input.audio_artifact_key has its stored artifact written to a
// local temp file, with input.audio_local_path injected and a cleanup that
// removes the file.
func TestMaterializeAudioArtifactWritesTempAndInjectsPath(t *testing.T) {
	store := artifacts.NewMemoryArtifactStore()
	ctx := context.Background()
	const jobID = "job_audio_1"
	const key = "dictation.webm"
	want := []byte("fake-opus-bytes")
	if _, err := store.PutArtifact(ctx, jobID, key, "audio/webm", bytes.NewReader(want), int64(len(want))); err != nil {
		t.Fatalf("put artifact: %v", err)
	}

	runner := ProcessWorkerRunner{Artifacts: store}
	env := &DispatchEnvelope{
		JobID: jobID,
		Job:   DispatchJob{Input: map[string]any{"audio_artifact_key": key, "prompt": "transcribe"}},
	}
	cleanup, err := runner.materializeAttachments(ctx, env)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected a cleanup func for an audio job")
	}

	pathVal, _ := env.Job.Input["audio_local_path"].(string)
	if pathVal == "" {
		t.Fatalf("audio_local_path not injected: %#v", env.Job.Input["audio_local_path"])
	}
	got, err := os.ReadFile(pathVal)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("temp file bytes = %q, want %q", got, want)
	}
	if !strings.HasSuffix(pathVal, ".webm") {
		t.Fatalf("temp file should keep the .webm extension: %s", pathVal)
	}

	cleanup()
	if _, err := os.Stat(pathVal); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove the temp file, stat err = %v", err)
	}
}

// A text job (no audio_artifact_key) is untouched: no cleanup, no injected path.
func TestMaterializeAudioArtifactNoopForTextJob(t *testing.T) {
	runner := ProcessWorkerRunner{Artifacts: artifacts.NewMemoryArtifactStore()}
	env := &DispatchEnvelope{JobID: "job_text", Job: DispatchJob{Input: map[string]any{"prompt": "hello"}}}
	cleanup, err := runner.materializeAttachments(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cleanup != nil {
		t.Fatal("text job should not produce a cleanup func")
	}
	if _, ok := env.Job.Input["audio_local_path"]; ok {
		t.Fatal("text job should not get audio_local_path")
	}
}

// With no store wired, materialization is a no-op even for an audio job.
func TestMaterializeAudioArtifactNilStore(t *testing.T) {
	runner := ProcessWorkerRunner{}
	env := &DispatchEnvelope{JobID: "j", Job: DispatchJob{Input: map[string]any{"audio_artifact_key": "x.webm"}}}
	cleanup, err := runner.materializeAttachments(context.Background(), env)
	if err != nil || cleanup != nil {
		t.Fatalf("nil store should be a no-op (cleanupSet=%v err=%v)", cleanup != nil, err)
	}
}

// A job declaring multiple attachments materializes each to its own temp file,
// injects attachment_local_paths in declared order, and cleans up all of them.
func TestMaterializeAttachmentsMultiFile(t *testing.T) {
	store := artifacts.NewMemoryArtifactStore()
	ctx := context.Background()
	const jobID = "job_multi_1"
	files := []struct {
		key, ct string
		body    []byte
	}{
		{"report.pdf", "application/pdf", []byte("%PDF-1.7 fake")},
		{"note.webm", "audio/webm", []byte("fake-opus")},
	}
	for _, f := range files {
		if _, err := store.PutArtifact(ctx, jobID, f.key, f.ct, bytes.NewReader(f.body), int64(len(f.body))); err != nil {
			t.Fatalf("put %s: %v", f.key, err)
		}
	}

	runner := ProcessWorkerRunner{Artifacts: store}
	env := &DispatchEnvelope{JobID: jobID, Job: DispatchJob{Input: map[string]any{
		"prompt": "summarize",
		"attachments": []any{
			map[string]any{"key": "report.pdf", "content_type": "application/pdf", "kind": "document"},
			map[string]any{"key": "note.webm", "content_type": "audio/webm", "kind": "voice"},
		},
	}}}
	cleanup, err := runner.materializeAttachments(ctx, env)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected cleanup func")
	}

	paths, _ := env.Job.Input["attachment_local_paths"].([]any)
	if len(paths) != 2 {
		t.Fatalf("expected 2 local paths, got %#v", env.Job.Input["attachment_local_paths"])
	}
	for i, f := range files {
		p, _ := paths[i].(string)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if !bytes.Equal(got, f.body) {
			t.Fatalf("path %d bytes = %q, want %q", i, got, f.body)
		}
	}

	cleanup()
	for _, p := range paths {
		if _, err := os.Stat(p.(string)); !os.IsNotExist(err) {
			t.Fatalf("cleanup should remove %v", p)
		}
	}
}

// When a later attachment is missing, materialize fails closed and removes any
// temp files it already wrote for earlier attachments (no orphaned temps).
func TestMaterializeAttachmentsPartialFailureCleansUp(t *testing.T) {
	store := artifacts.NewMemoryArtifactStore()
	ctx := context.Background()
	const jobID = "job_partial_1"
	present := []byte("here")
	if _, err := store.PutArtifact(ctx, jobID, "a.pdf", "application/pdf", bytes.NewReader(present), int64(len(present))); err != nil {
		t.Fatalf("put a.pdf: %v", err)
	}

	runner := ProcessWorkerRunner{Artifacts: store}
	env := &DispatchEnvelope{JobID: jobID, Job: DispatchJob{Input: map[string]any{
		"attachments": []any{
			map[string]any{"key": "a.pdf", "content_type": "application/pdf", "kind": "document"},
			map[string]any{"key": "missing.pdf", "content_type": "application/pdf", "kind": "document"},
		},
	}}}
	cleanup, err := runner.materializeAttachments(ctx, env)
	if err == nil {
		t.Fatal("expected error for a missing artifact")
	}
	if cleanup != nil {
		t.Fatal("no cleanup func should be returned on failure (temps already removed)")
	}
	if _, ok := env.Job.Input["attachment_local_paths"]; ok {
		t.Fatal("attachment_local_paths must not be injected on failure")
	}
}

func TestMaterializeAttachmentsCountsArtifactReadFailures(t *testing.T) {
	before := attachmentMaterializeFailureSnapshot()["artifact_read"]
	runner := ProcessWorkerRunner{Artifacts: artifacts.NewMemoryArtifactStore()}
	env := &DispatchEnvelope{JobID: "job_metric_missing", Job: DispatchJob{Input: map[string]any{
		"attachments": []any{
			map[string]any{"key": "missing.pdf", "content_type": "application/pdf", "kind": "document"},
		},
	}}}
	if _, err := runner.materializeAttachments(context.Background(), env); err == nil {
		t.Fatal("expected missing artifact error")
	}
	after := attachmentMaterializeFailureSnapshot()["artifact_read"]
	if after != before+1 {
		t.Fatalf("artifact_read failures = %d, want %d", after, before+1)
	}
}

func TestMaterializeAttachmentsPreservesSafeDeclaredFilename(t *testing.T) {
	store := artifacts.NewMemoryArtifactStore()
	body := []byte("pdf")
	if _, err := store.PutArtifact(context.Background(), "job_filename", "opaque", "application/pdf", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	env := &DispatchEnvelope{JobID: "job_filename", Job: DispatchJob{Input: map[string]any{
		"attachments": []any{
			map[string]any{"key": "opaque", "filename": "Quarterly Report.pdf", "content_type": "application/pdf", "kind": "document"},
		},
	}}}
	cleanup, err := (ProcessWorkerRunner{Artifacts: store}).materializeAttachments(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	paths := env.Job.Input["attachment_local_paths"].([]any)
	if got := filepath.Base(paths[0].(string)); got != "Quarterly Report.pdf" {
		t.Fatalf("materialized filename = %q, want declared filename", got)
	}
}

func TestAttachmentMIMEExtensionFallbacks(t *testing.T) {
	tests := map[string]string{
		"text/markdown": ".md",
		"text/csv":      ".csv",
		"video/mp4":     ".mp4",
		"video/webm":    ".webm",
	}
	for contentType, want := range tests {
		if got := extForContentType(contentType); got != want {
			t.Errorf("extForContentType(%q) = %q, want %q", contentType, got, want)
		}
	}
}
