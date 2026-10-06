package staging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/appjwt"
	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/attemptcap"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helperapi"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
)

type rig struct {
	arts   *artifacts.MemoryArtifactStore
	job    jobs.Job
	client *StagingClient
	srv    *httptest.Server
}

// newRig wires a real helperapi.Assets behind httptest and a StagingClient to it.
func newRig(t *testing.T, wrap func(artifacts.ArtifactStore) artifacts.ArtifactStore) *rig {
	t.Helper()
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	js, as := jobs.NewMemoryStore(), artifacts.NewMemoryArtifactStore()
	job, err := js.Create(context.Background(), jobs.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app", Target: "mock", CommandType: "submit",
		Input: map[string]any{"prompt": "p", "attachments": []any{
			map[string]any{"key": "report.pdf", "content_type": "application/pdf", "kind": "document"},
			map[string]any{"key": "note.webm", "content_type": "audio/webm", "kind": "voice"},
		}},
		TraceID: "trace_stage",
	})
	if err != nil {
		t.Fatal(err)
	}
	att, err := js.BeginAttempt(context.Background(), jobs.BeginAttemptRequest{
		JobID: job.ID, AttemptID: "att_stage", NodeID: "helper-1", InputFingerprint: "fp", WorkloadVersion: "w",
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := attemptcap.Issue(att, job.TenantID, []string{"report.pdf", "note.webm", "out.png"},
		[]string{attemptcap.OpRead, attemptcap.OpWrite}, time.Minute, time.Now(), priv)
	if err != nil {
		t.Fatal(err)
	}
	var served artifacts.ArtifactStore = as
	if wrap != nil {
		served = wrap(as)
	}
	srv := httptest.NewServer(&helperapi.Assets{
		Attempts: js, Jobs: js, Artifacts: served, TokenKey: &priv.PublicKey,
		Authn: func(*http.Request) (helperauth.Identity, error) { return helperauth.Identity{NodeID: "helper-1"}, nil },
	})
	t.Cleanup(srv.Close)
	return &rig{arts: as, job: job, srv: srv, client: &StagingClient{
		BaseURL: srv.URL, HTTP: srv.Client(), JobID: job.ID, Token: func() string { return tok },
	}}
}

func (r *rig) seed(t *testing.T, key, ct string, body []byte) {
	t.Helper()
	if _, err := r.arts.PutArtifact(context.Background(), r.job.ID, key, ct, bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
}

func envelope(r *rig) *executor.DispatchEnvelope {
	return &executor.DispatchEnvelope{JobID: r.job.ID, Job: executor.DispatchJob{Input: r.job.Input}}
}

// The staging store plugs into the worker runner unchanged: files land in
// ubag-attach-* temp dirs (the worker path guard requires that prefix), bytes
// are verified, and cleanup removes everything.
func TestStagingMaterializesThroughWorkerRunner(t *testing.T) {
	r := newRig(t, nil)
	pdf, voice := []byte("%PDF-1.7 staged"), []byte("opus-bytes")
	r.seed(t, "report.pdf", "application/pdf", pdf)
	r.seed(t, "note.webm", "audio/webm", voice)

	env := envelope(r)
	cleanup, err := (executor.ProcessWorkerRunner{Artifacts: ReadOnlyStore{r.client}}).MaterializeAttachments(context.Background(), env)
	if err != nil || cleanup == nil {
		t.Fatalf("materialize: cleanup=%v err=%v", cleanup != nil, err)
	}
	paths, _ := env.Job.Input["attachment_local_paths"].([]any)
	if len(paths) != 2 {
		t.Fatalf("paths = %#v", paths)
	}
	for i, want := range [][]byte{pdf, voice} {
		p := paths[i].(string)
		if !strings.HasPrefix(filepath.Base(filepath.Dir(p)), "ubag-attach-") {
			t.Fatalf("temp dir lost the ubag-attach- prefix: %s", p)
		}
		if got, err := os.ReadFile(p); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("file %s = %q, %v", p, got, err)
		}
	}
	cleanup()
	if _, err := os.Stat(paths[0].(string)); !os.IsNotExist(err) {
		t.Fatalf("cleanup left the file behind: %v", err)
	}
}

type corrupt struct {
	artifacts.ArtifactStore
	mode string
}

func (c corrupt) GetArtifact(ctx context.Context, job, key string) (io.ReadCloser, artifacts.ArtifactRecord, error) {
	rc, rec, err := c.ArtifactStore.GetArtifact(ctx, job, key)
	if err == nil && c.mode == "sha" {
		rec.Checksum = strings.Repeat("0", 64)
	}
	return rc, rec, err
}

// A digest the bytes do not match must fail materialization and leave no files.
func TestStagingChecksumMismatchFailsClosed(t *testing.T) {
	r := newRig(t, func(s artifacts.ArtifactStore) artifacts.ArtifactStore { return corrupt{s, "sha"} })
	r.seed(t, "report.pdf", "application/pdf", []byte("pdf"))
	r.seed(t, "note.webm", "audio/webm", []byte("opus"))
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "ubag-attach-*"))

	env := envelope(r)
	cleanup, err := (executor.ProcessWorkerRunner{Artifacts: ReadOnlyStore{r.client}}).MaterializeAttachments(context.Background(), env)
	if err == nil || cleanup != nil {
		t.Fatalf("tampered stream was accepted (err=%v)", err)
	}
	if _, ok := env.Job.Input["attachment_local_paths"]; ok {
		t.Fatal("paths injected despite failure")
	}
	if after, _ := filepath.Glob(filepath.Join(os.TempDir(), "ubag-attach-*")); len(after) != len(before) {
		t.Fatalf("temp attachment dirs leaked: %d -> %d", len(before), len(after))
	}
}

// Client-side verification stands on its own: a wrong size or digest from the
// wire is ErrIntegrity, never a clean EOF.
func TestStagingClientVerifiesSizeAndDigest(t *testing.T) {
	body := []byte("0123456789")
	sum := sha256.Sum256(body)
	for name, tc := range map[string]struct {
		length int
		digest string
		sent   []byte
	}{
		"short":  {len(body), hex.EncodeToString(sum[:]), body[:5]},
		"digest": {len(body), strings.Repeat("a", 64), body},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(helperapi.HeaderSHA256, tc.digest)
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write(tc.sent)
		}))
		c := &StagingClient{BaseURL: srv.URL, HTTP: srv.Client(), JobID: "j", Token: func() string { return "t" }}
		rc, _, err := c.Get(context.Background(), "k")
		if err == nil {
			_, err = io.ReadAll(rc)
			rc.Close()
		}
		srv.Close()
		if err == nil {
			t.Fatalf("%s: corrupted stream verified", name)
		}
	}
	// over the client's cap is refused before any byte is read.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(helperapi.HeaderSHA256, hex.EncodeToString(sum[:]))
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := &StagingClient{BaseURL: srv.URL, HTTP: srv.Client(), JobID: "j", Token: func() string { return "t" }, MaxBytes: 5}
	if _, _, err := c.Get(context.Background(), "k"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("over-cap err = %v", err)
	}
}

// The adapter is read-only and bound to its own job; unauthorised keys read as
// plain not-found.
func TestStagingReadOnlyStore(t *testing.T) {
	r := newRig(t, nil)
	r.seed(t, "report.pdf", "application/pdf", []byte("pdf"))
	store := ReadOnlyStore{r.client}
	ctx := context.Background()
	if _, err := store.PutArtifact(ctx, r.job.ID, "x", "text/plain", strings.NewReader("x"), 1); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("put err = %v", err)
	}
	if err := store.DeleteArtifact(ctx, r.job.ID, "report.pdf"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("delete err = %v", err)
	}
	if _, err := store.ListArtifacts(ctx, r.job.ID); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("list err = %v", err)
	}
	if _, _, err := store.GetArtifact(ctx, "other_job", "report.pdf"); !artifacts.IsNotFound(err) {
		t.Fatalf("other job err = %v", err)
	}
	if _, _, err := store.GetArtifact(ctx, r.job.ID, "ghost"); !artifacts.IsNotFound(err) {
		t.Fatalf("undeclared key err = %v", err)
	}
}

// Output assets travel the same verified channel.
func TestStagingPut(t *testing.T) {
	r := newRig(t, nil)
	out := []byte("png-out")
	s := sha256.Sum256(out)
	if err := r.client.Put(context.Background(), "out.png", "image", "image/png", hex.EncodeToString(s[:]), bytes.NewReader(out), int64(len(out))); err != nil {
		t.Fatalf("put: %v", err)
	}
	rc, rec, err := r.arts.GetArtifact(context.Background(), r.job.ID, "out.png")
	if err != nil || rec.Checksum != hex.EncodeToString(s[:]) {
		t.Fatalf("stored: %v %+v", err, rec)
	}
	rc.Close()
	if err := r.client.Put(context.Background(), "out.png", "image", "image/png", hex.EncodeToString(s[:]), bytes.NewReader(out), int64(len(out))); err == nil {
		t.Fatal("overwrite succeeded")
	}
}
