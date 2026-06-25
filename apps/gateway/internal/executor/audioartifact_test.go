package executor

import (
	"bytes"
	"context"
	"os"
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
	cleanup, err := runner.materializeAudioArtifact(ctx, env)
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
	cleanup, err := runner.materializeAudioArtifact(context.Background(), env)
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
	cleanup, err := runner.materializeAudioArtifact(context.Background(), env)
	if err != nil || cleanup != nil {
		t.Fatalf("nil store should be a no-op (cleanupSet=%v err=%v)", cleanup != nil, err)
	}
}
