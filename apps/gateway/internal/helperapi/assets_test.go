package helperapi

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/appjwt"
	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/attemptcap"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
)

const node = "helper-1"

type env struct {
	t     *testing.T
	jobs  *jobs.MemoryStore
	arts  *artifacts.MemoryArtifactStore
	priv  *rsa.PrivateKey
	job   jobs.Job
	att   jobs.Attempt
	h     *Assets
	nowAt time.Time
	node  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	priv, err := appjwt.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, jobs: jobs.NewMemoryStore(), arts: artifacts.NewMemoryArtifactStore(), priv: priv, node: node}
	e.job = e.newJob("tenant_a", "report.pdf")
	e.att, err = e.jobs.BeginAttempt(context.Background(), jobs.BeginAttemptRequest{
		JobID: e.job.ID, AttemptID: "att_one", NodeID: node, InputFingerprint: "fp", WorkloadVersion: "w",
	})
	if err != nil {
		t.Fatal(err)
	}
	e.h = &Assets{
		Attempts: e.jobs, Jobs: e.jobs, Artifacts: e.arts, TokenKey: &priv.PublicKey,
		Authn: func(*http.Request) (helperauth.Identity, error) { return helperauth.Identity{NodeID: e.node}, nil },
		Now:   func() time.Time { return e.nowAt },
	}
	return e
}

func (e *env) newJob(tenant, declaredKey string) jobs.Job {
	job, err := e.jobs.Create(context.Background(), jobs.CreateRequest{
		APIVersion: "2026-05-22", TenantID: tenant, AppID: "app", Target: "mock", CommandType: "submit",
		Input: map[string]any{"prompt": "x", "attachments": []any{
			map[string]any{"key": declaredKey, "content_type": "application/pdf", "kind": "document"},
		}},
		TraceID: "trace_" + tenant + declaredKey,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return job
}

func (e *env) put(jobID, key string, body []byte) artifacts.ArtifactRecord {
	rec, err := e.arts.PutArtifact(context.Background(), jobID, key, "application/pdf", bytes.NewReader(body), int64(len(body)))
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

func (e *env) token(keys, ops []string) string {
	tok, err := attemptcap.Issue(e.att, e.job.TenantID, keys, ops, time.Minute, time.Now(), e.priv)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *env) do(method, key, token string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	if e.nowAt.IsZero() {
		e.nowAt = time.Now()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, PathPrefix+key, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func putHeaders(body []byte, kind string) map[string]string {
	return map[string]string{"Content-Type": "image/png", HeaderKind: kind, HeaderSHA256: sum(body)}
}

func TestStagingGetStreamsVerifiedBytes(t *testing.T) {
	e := newEnv(t)
	body := []byte("%PDF-1.7 payload")
	e.put(e.job.ID, "report.pdf", body)
	rec := e.do(http.MethodGet, "report.pdf", e.token([]string{"report.pdf"}, []string{attemptcap.OpRead}), nil, nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("get = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get(HeaderSHA256) != sum(body) || rec.Header().Get("Content-Length") != "16" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

// Every denial must be byte-identical: probing cannot separate "exists but not
// yours" from "does not exist".
func TestStagingDenialsAreIndistinguishable(t *testing.T) {
	e := newEnv(t)
	e.put(e.job.ID, "report.pdf", []byte("pdf"))
	other := e.newJob("tenant_b", "secret.pdf")
	e.put(other.ID, "secret.pdf", []byte("other tenant"))
	read := []string{attemptcap.OpRead}
	getOnly := e.token([]string{"report.pdf", "secret.pdf", "ghost.pdf", "new.png"}, read)
	rw := e.token([]string{"report.pdf"}, []string{attemptcap.OpRead, attemptcap.OpWrite})
	putOnly := e.token([]string{"report.pdf"}, []string{attemptcap.OpWrite})
	wrongTenant, err := attemptcap.Issue(e.att, "tenant_evil", []string{"report.pdf"}, read, time.Minute, time.Now(), e.priv)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, _ := appjwt.GenerateKeyPair()
	forged, err := attemptcap.Issue(e.att, e.job.TenantID, []string{"report.pdf"}, read, time.Minute, time.Now(), otherKey)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		method string
		key    string
		token  string
		node   string
		now    time.Time
	}{
		{"unknown key", "GET", "ghost.pdf", getOnly, node, time.Time{}},
		{"key outside the manifest", "GET", "new.png", getOnly, node, time.Time{}},
		{"key outside the token", "GET", "report.pdf", e.token([]string{"other.pdf"}, read), node, time.Time{}},
		{"another job's key", "GET", "secret.pdf", getOnly, node, time.Time{}},
		{"put on a get-only token", "PUT", "new.png", getOnly, node, time.Time{}},
		{"get on a put-only token", "GET", "report.pdf", putOnly, node, time.Time{}},
		{"put over a declared input", "PUT", "report.pdf", rw, node, time.Time{}},
		{"expired lease", "GET", "report.pdf", rw, node, time.Now().Add(2 * time.Minute)},
		{"wrong node", "GET", "report.pdf", rw, "helper-2", time.Time{}},
		{"wrong tenant", "GET", "report.pdf", wrongTenant, node, time.Time{}},
		{"forged signature", "GET", "report.pdf", forged, node, time.Time{}},
		{"no token", "GET", "report.pdf", "", node, time.Time{}},
		{"bad key", "GET", "a%2Fb", rw, node, time.Time{}},
		{"unsupported method", "DELETE", "report.pdf", rw, node, time.Time{}},
	}
	var first *httptest.ResponseRecorder
	for _, tc := range cases {
		e.node = tc.node
		e.nowAt = tc.now
		var body []byte
		hdr := map[string]string(nil)
		if tc.method == "PUT" {
			body = []byte("png")
			hdr = putHeaders(body, "image")
		}
		rec := e.do(tc.method, tc.key, tc.token, body, hdr)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d body %q", tc.name, rec.Code, rec.Body.String())
		}
		if first == nil {
			first = rec
			continue
		}
		if rec.Body.String() != first.Body.String() || rec.Header().Get("Content-Type") != first.Header().Get("Content-Type") {
			t.Fatalf("%s: response differs: %q vs %q", tc.name, rec.Body.String(), first.Body.String())
		}
	}
	if string(first.Body.Bytes()) != string(notFoundBody) {
		t.Fatalf("shape = %q", first.Body.String())
	}
	// the other tenant's bytes were never written to or exposed.
	if _, _, err := e.arts.GetArtifact(context.Background(), e.job.ID, "new.png"); !artifacts.IsNotFound(err) {
		t.Fatalf("denied put created an artifact: %v", err)
	}
}

func TestStagingReapedAttemptIsFenced(t *testing.T) {
	e := newEnv(t)
	e.put(e.job.ID, "report.pdf", []byte("pdf"))
	job := e.newJob("tenant_a", "report.pdf")
	e.put(job.ID, "report.pdf", []byte("pdf"))
	att, err := e.jobs.BeginAttempt(context.Background(), jobs.BeginAttemptRequest{
		JobID: job.ID, AttemptID: "att_short", NodeID: node, TTL: time.Second, InputFingerprint: "fp", WorkloadVersion: "w",
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := attemptcap.Issue(att, job.TenantID, []string{"report.pdf"}, []string{attemptcap.OpRead}, time.Minute, time.Now(), e.priv)
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", "report.pdf", tok, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("live attempt got %d", rec.Code)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := e.jobs.ExpireAttempts(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	e.nowAt = time.Now()
	if rec := e.do("GET", "report.pdf", tok, nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("fenced attempt got %d", rec.Code)
	}
}

type badSumStore struct {
	*artifacts.MemoryArtifactStore
	sum   string
	extra int64
}

func (s badSumStore) GetArtifact(ctx context.Context, job, key string) (io.ReadCloser, artifacts.ArtifactRecord, error) {
	rc, rec, err := s.MemoryArtifactStore.GetArtifact(ctx, job, key)
	rec.Checksum = s.sum
	rec.SizeBytes += s.extra
	return rc, rec, err
}

func TestStagingGetFailsClosedOnChecksumAndSize(t *testing.T) {
	zero := strings.Repeat("0", 64)
	for name, store := range map[string]func(*artifacts.MemoryArtifactStore) artifacts.ArtifactStore{
		"checksum mismatch": func(m *artifacts.MemoryArtifactStore) artifacts.ArtifactStore { return badSumStore{m, zero, 0} },
		"missing checksum":  func(m *artifacts.MemoryArtifactStore) artifacts.ArtifactStore { return badSumStore{m, "", 0} },
	} {
		e := newEnv(t)
		e.put(e.job.ID, "report.pdf", []byte("pdf-bytes"))
		e.h.Artifacts = store(e.arts)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.URL.Path = PathPrefix + "report.pdf"
			e.nowAt = time.Now()
			e.h.ServeHTTP(w, r)
		}))
		req, _ := http.NewRequest("GET", srv.URL+PathPrefix+"report.pdf", nil)
		req.Header.Set("Authorization", "Bearer "+e.token([]string{"report.pdf"}, []string{attemptcap.OpRead}))
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if name == "missing checksum" {
				if resp.StatusCode != http.StatusInternalServerError {
					t.Fatalf("%s: status %d", name, resp.StatusCode)
				}
				err = errors.New("ok")
			}
		}
		if err == nil {
			t.Fatalf("%s: stream ended cleanly", name)
		}
		srv.Close()
	}
}

func TestStagingGetSizeCap(t *testing.T) {
	e := newEnv(t)
	e.put(e.job.ID, "report.pdf", bytes.Repeat([]byte("a"), 100))
	e.h.MaxBytes = 99
	rec := e.do("GET", "report.pdf", e.token([]string{"report.pdf"}, []string{attemptcap.OpRead}), nil, nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap get = %d", rec.Code)
	}
}

func TestStagingPutVerifiesAndRejects(t *testing.T) {
	e := newEnv(t)
	tok := e.token([]string{"out.png", "bad.png", "huge.png", "kind.png"}, []string{attemptcap.OpWrite})
	body := []byte("png-bytes")

	rec := e.do("PUT", "out.png", tok, body, putHeaders(body, "image"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("put = %d %s", rec.Code, rec.Body.String())
	}
	stored, r, err := e.arts.GetArtifact(context.Background(), e.job.ID, "out.png")
	if err != nil || r.Checksum != sum(body) {
		t.Fatalf("stored: %v %+v", err, r)
	}
	stored.Close()
	if rec := e.do("PUT", "out.png", tok, body, putHeaders(body, "image")); rec.Code != http.StatusConflict {
		t.Fatalf("overwrite = %d", rec.Code)
	}

	// sha256 mismatch: 422 and nothing stays readable.
	h := putHeaders(body, "image")
	h[HeaderSHA256] = strings.Repeat("1", 64)
	if rec := e.do("PUT", "bad.png", tok, body, h); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch = %d", rec.Code)
	}
	if _, _, err := e.arts.GetArtifact(context.Background(), e.job.ID, "bad.png"); !artifacts.IsNotFound(err) {
		t.Fatalf("mismatched upload remained: %v", err)
	}

	// size cap, by declared length and by actual body.
	e.h.MaxBytes = 4
	if rec := e.do("PUT", "huge.png", tok, body, putHeaders(body, "image")); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over cap = %d", rec.Code)
	}
	e.h.MaxBytes = 0

	// undeclared kinds are rejected, valid ones are not.
	for _, kind := range []string{"", "executable", "screenshot-raw"} {
		if rec := e.do("PUT", "kind.png", tok, body, putHeaders(body, kind)); rec.Code != http.StatusBadRequest {
			t.Fatalf("kind %q = %d", kind, rec.Code)
		}
	}
}
