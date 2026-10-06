// Package helper holds Helper Node side code (perf-fleet). staging.go is the
// asset staging client (P4.10): it fetches an attempt's declared attachments
// from the primary's helperapi.Assets endpoint over mTLS, verifies size and
// SHA-256 as the bytes stream, and exposes them as a READ-ONLY
// artifacts.ArtifactStore so executor.ProcessWorkerRunner.MaterializeAttachments
// (and its ubag-attach- temp prefix the worker path guard requires) is reused
// unchanged.
package helper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/helperapi"
)

var (
	// ErrIntegrity: the bytes received do not match the advertised size or
	// SHA-256 (or the cap was exceeded). The staged file must be discarded.
	ErrIntegrity = errors.New("helper: staged asset failed integrity verification")
	// ErrReadOnly: the staging store never writes, lists or deletes.
	ErrReadOnly = errors.New("helper: staging store is read-only")
)

// StagingClient talks to one primary for one attempt. HTTP must already carry
// the node's mTLS client certificate.
type StagingClient struct {
	BaseURL string // e.g. https://primary.internal:8443 (no trailing path)
	HTTP    *http.Client
	JobID   string
	// Token returns the current attempt token (it is renewed with the lease).
	Token func() string
	// MaxBytes caps one asset; <= 0 means helperapi.DefaultMaxBytes.
	MaxBytes int64
}

func (c *StagingClient) maxBytes() int64 {
	if c.MaxBytes > 0 {
		return c.MaxBytes
	}
	return helperapi.DefaultMaxBytes
}

func (c *StagingClient) do(ctx context.Context, method, key string, body io.Reader, hdr map[string]string, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+helperapi.PathPrefix+url.PathEscape(key), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.ContentLength = size
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	return hc.Do(req)
}

// Get streams one declared attachment. The returned reader yields ErrIntegrity
// instead of io.EOF when the stream ends short, long or with the wrong SHA-256,
// so a consumer that copies to completion (materializeAttachments does) fails
// closed. A 404 of any cause maps to artifacts.ErrArtifactNotFound.
func (c *StagingClient) Get(ctx context.Context, key string) (io.ReadCloser, artifacts.ArtifactRecord, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil, 0)
	if err != nil {
		return nil, artifacts.ArtifactRecord{}, err
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, artifacts.ArtifactRecord{}, &artifacts.ErrArtifactNotFound{JobID: c.JobID, Key: key}
		}
		return nil, artifacts.ArtifactRecord{}, fmt.Errorf("helper: staging get %q: status %d", key, resp.StatusCode)
	}
	sum := strings.ToLower(resp.Header.Get(helperapi.HeaderSHA256))
	if raw, err := hex.DecodeString(sum); err != nil || len(raw) != sha256.Size ||
		resp.ContentLength < 0 || resp.ContentLength > c.maxBytes() {
		_ = resp.Body.Close()
		return nil, artifacts.ArtifactRecord{}, fmt.Errorf("%w: missing digest or size over cap", ErrIntegrity)
	}
	rec := artifacts.ArtifactRecord{JobID: c.JobID, Key: key, SizeBytes: resp.ContentLength, Checksum: sum,
		ContentType: resp.Header.Get("Content-Type")}
	return &verifyingReader{rc: resp.Body, h: sha256.New(), want: sum, size: resp.ContentLength}, rec, nil
}

// Put uploads one output asset. sha256Hex is the digest of the size bytes in r;
// the primary re-verifies and rejects (422) any mismatch.
func (c *StagingClient) Put(ctx context.Context, key, kind, contentType, sha256Hex string, r io.Reader, size int64) error {
	if size < 0 || size > c.maxBytes() {
		return fmt.Errorf("helper: staging put %q: size over cap", key)
	}
	resp, err := c.do(ctx, http.MethodPut, key, r, map[string]string{
		"Content-Type": contentType, helperapi.HeaderKind: kind, helperapi.HeaderSHA256: sha256Hex,
	}, size)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("helper: staging put %q: status %d", key, resp.StatusCode)
	}
	return nil
}

type verifyingReader struct {
	rc   io.ReadCloser
	h    hash.Hash
	want string
	size int64
	n    int64
	done bool
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	if v.done {
		return 0, io.EOF
	}
	n, err := v.rc.Read(p)
	v.h.Write(p[:n])
	v.n += int64(n)
	if v.n > v.size {
		return n, ErrIntegrity
	}
	switch {
	case err == io.EOF:
		v.done = true
		if v.n != v.size || hex.EncodeToString(v.h.Sum(nil)) != v.want {
			return n, ErrIntegrity
		}
		return n, io.EOF
	case err != nil:
		return n, fmt.Errorf("%w: %v", ErrIntegrity, err)
	}
	return n, nil
}

func (v *verifyingReader) Close() error { return v.rc.Close() }

// ReadOnlyStore adapts a StagingClient to artifacts.ArtifactStore for the
// worker runner. Only GetArtifact for the client's own job works.
type ReadOnlyStore struct{ Client *StagingClient }

func (s ReadOnlyStore) Ready(context.Context) error { return nil }

func (s ReadOnlyStore) GetArtifact(ctx context.Context, jobID, key string) (io.ReadCloser, artifacts.ArtifactRecord, error) {
	if jobID != s.Client.JobID {
		return nil, artifacts.ArtifactRecord{}, &artifacts.ErrArtifactNotFound{JobID: jobID, Key: key}
	}
	return s.Client.Get(ctx, key)
}

func (ReadOnlyStore) PutArtifact(context.Context, string, string, string, io.Reader, int64) (artifacts.ArtifactRecord, error) {
	return artifacts.ArtifactRecord{}, ErrReadOnly
}

func (ReadOnlyStore) ListArtifacts(context.Context, string) ([]artifacts.ArtifactRecord, error) {
	return nil, ErrReadOnly
}

func (ReadOnlyStore) DeleteArtifact(context.Context, string, string) error { return ErrReadOnly }

var _ artifacts.ArtifactStore = ReadOnlyStore{}
