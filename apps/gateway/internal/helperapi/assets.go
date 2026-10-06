// Package helperapi serves the Helper Node HTTP surface of the trust plane
// (perf-fleet P4.10): manifest-bound, checksummed asset get and put for one
// attempt. It sits behind the helperauth mTLS listener (UBAG_HELPER_PLANE,
// default off) and is mounted by a later slice; nothing here binds a socket.
//
// Authorisation is layered and every layer fails closed:
//
//  1. mTLS node identity (Assets.Authn, normally helperauth);
//  2. an attempt capability token (attemptcap): signed, bound to that node,
//     attempt, lease generation, job, tenant, key set and op set, and checked
//     against the LIVE attempt row, so a superseded attempt loses access at once;
//  3. the manifest: a read must name a key the job declared in its input
//     (attachments.DeclaredAttachments), a write must NOT name one (declared
//     inputs are immutable) and must carry a valid artifact kind.
//
// The job id never comes from the URL or a header: it is the token's, so a
// helper cannot address another job's artifacts. ArtifactStore has no tenant
// parameter, so the tenant bind lives here: the token's tenant must equal the
// tenant of the job row loaded for it.
//
// Every denial (no/invalid/expired token, wrong node, fenced attempt, wrong op,
// key outside the token or manifest, unknown key) returns the SAME 404 body, so
// a probing helper cannot tell "exists but not yours" from "does not exist".
package helperapi

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/attachments"
	"github.com/ubag/ubag/apps/gateway/internal/attemptcap"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
)

const (
	// PathPrefix is where Assets is mounted; the key follows it.
	PathPrefix = "/assets/"
	// HeaderSHA256 carries the lowercase-hex SHA-256 of the body (response on
	// get, required on put).
	HeaderSHA256 = "X-Ubag-Sha256"
	// HeaderKind carries the artifact kind of a put (attachments.ValidKind).
	HeaderKind = "X-Ubag-Asset-Kind"

	// DefaultMaxBytes matches the public per-artifact upload cap.
	DefaultMaxBytes int64 = 32 << 20

	CodeNotFound  = "UBAG-HELPER-ASSET-NOT-FOUND-001"
	CodeIntegrity = "UBAG-HELPER-ASSET-INTEGRITY-001"
	CodeTooLarge  = "UBAG-HELPER-ASSET-TOO-LARGE-001"
	CodeInvalid   = "UBAG-HELPER-ASSET-INVALID-001"
	CodeExists    = "UBAG-HELPER-ASSET-EXISTS-001"
	CodeInternal  = "UBAG-HELPER-ASSET-INTERNAL-001"
)

// notFoundBody is the one response every denial shares, byte for byte.
var notFoundBody = []byte(`{"error":{"code":"` + CodeNotFound + `","message":"asset not found"}}` + "\n")

// JobLookup is the slice of jobs.Store the handler reads.
type JobLookup interface {
	Get(ctx context.Context, id string) (jobs.Job, bool, error)
}

// Assets is the http.Handler for PathPrefix. All fields except Now, MaxBytes
// and Authn are required.
type Assets struct {
	Attempts  jobs.AttemptStore
	Jobs      JobLookup
	Artifacts artifacts.ArtifactStore
	TokenKey  *rsa.PublicKey // attempt token verification key
	// Authn resolves the mTLS node of the request; AuthnFromAuthenticator is
	// the production implementation.
	Authn func(*http.Request) (helperauth.Identity, error)
	// MaxBytes caps one asset in either direction; <= 0 means DefaultMaxBytes.
	MaxBytes int64
	Now      func() time.Time
}

// AuthnFromAuthenticator re-verifies the TLS client certificate of every request
// (registry, pin, revocation, expiry), not just at handshake time.
func AuthnFromAuthenticator(a *helperauth.Authenticator) func(*http.Request) (helperauth.Identity, error) {
	return func(r *http.Request) (helperauth.Identity, error) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			return helperauth.Identity{}, errors.New("helperapi: no client certificate")
		}
		return a.Verify(r.Context(), r.TLS.PeerCertificates[0])
	}
}

func (a *Assets) maxBytes() int64 {
	if a.MaxBytes > 0 {
		return a.MaxBytes
	}
	return DefaultMaxBytes
}

func (a *Assets) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func notFound(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(notFoundBody)
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func isCapDenial(err error) bool {
	for _, target := range []error{
		attemptcap.ErrInvalid, attemptcap.ErrExpired, attemptcap.ErrWrongNode, attemptcap.ErrWrongGeneration,
		attemptcap.ErrWrongBinding, attemptcap.ErrLeaseExceeded, attemptcap.ErrNotAllowed, attemptcap.ErrFenced,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func (a *Assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var op string
	switch r.Method {
	case http.MethodGet:
		op = attemptcap.OpRead
	case http.MethodPut:
		op = attemptcap.OpWrite
	default:
		notFound(w)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, PathPrefix)
	if !strings.HasPrefix(r.URL.Path, PathPrefix) || !attachments.ValidKey(key) || key != strings.TrimSpace(key) {
		notFound(w)
		return
	}
	id, err := a.Authn(r)
	if err != nil {
		notFound(w)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		notFound(w)
		return
	}
	now := a.now()
	claims, err := attemptcap.Peek(token, a.TokenKey, now)
	if err != nil {
		notFound(w)
		return
	}
	job, found, err := a.Jobs.Get(r.Context(), claims.Job)
	if err != nil {
		fail(w, http.StatusInternalServerError, CodeInternal, "asset service unavailable")
		return
	}
	if !found || job.TenantID == "" {
		notFound(w)
		return
	}
	// Tenant bind (ArtifactStore is not tenant-aware): Authorize requires the
	// token tenant to equal this job's tenant, node to equal the mTLS identity,
	// and the attempt to be the live one.
	if _, err := attemptcap.Authorize(r.Context(), a.Attempts, token, a.TokenKey, attemptcap.Request{
		Node: id.NodeID, Tenant: job.TenantID, Key: key, Op: op, Now: now,
	}); err != nil {
		if isCapDenial(err) {
			notFound(w)
			return
		}
		fail(w, http.StatusInternalServerError, CodeInternal, "asset service unavailable")
		return
	}
	declared, err := attachments.DeclaredAttachments(job.Input)
	if err != nil {
		notFound(w)
		return
	}
	isDeclared := false
	for _, d := range declared {
		if d.Key == key {
			isDeclared = true
			break
		}
	}
	if isDeclared != (op == attemptcap.OpRead) {
		// read of an undeclared key, or write over a declared input.
		notFound(w)
		return
	}
	if op == attemptcap.OpRead {
		a.get(w, r, claims.Job, key)
		return
	}
	a.put(w, r, claims.Job, key)
}

func (a *Assets) get(w http.ResponseWriter, r *http.Request, jobID, key string) {
	rc, rec, err := a.Artifacts.GetArtifact(r.Context(), jobID, key)
	if err != nil {
		if artifacts.IsNotFound(err) || artifacts.IsInvalid(err) {
			notFound(w)
			return
		}
		fail(w, http.StatusInternalServerError, CodeInternal, "asset service unavailable")
		return
	}
	defer rc.Close()
	if rec.SizeBytes < 0 || len(rec.Checksum) != sha256.Size*2 {
		// no trustworthy digest to serve against: fail closed.
		fail(w, http.StatusInternalServerError, CodeIntegrity, "asset has no verifiable checksum")
		return
	}
	if rec.SizeBytes > a.maxBytes() {
		fail(w, http.StatusRequestEntityTooLarge, CodeTooLarge, "asset exceeds the staging size cap")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(rec.SizeBytes, 10))
	h.Set(HeaderSHA256, strings.ToLower(rec.Checksum))
	h.Set("Cache-Control", "no-store")
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, sum), io.LimitReader(rc, rec.SizeBytes+1))
	if err != nil || n != rec.SizeBytes || hex.EncodeToString(sum.Sum(nil)) != strings.ToLower(rec.Checksum) {
		// The headers are gone, so the only honest signal is to kill the
		// stream; the helper also verifies and would fail on its own.
		panic(http.ErrAbortHandler)
	}
}

// hashingReader hashes and counts what the store consumes.
type hashingReader struct {
	r io.Reader
	h hash.Hash
	n int64
}

func (hr *hashingReader) Read(p []byte) (int, error) {
	n, err := hr.r.Read(p)
	hr.h.Write(p[:n])
	hr.n += int64(n)
	return n, err
}

func (a *Assets) put(w http.ResponseWriter, r *http.Request, jobID, key string) {
	if !attachments.ValidKind(r.Header.Get(HeaderKind)) {
		fail(w, http.StatusBadRequest, CodeInvalid, "asset kind is not allowed")
		return
	}
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if mt, _, err := mime.ParseMediaType(contentType); err != nil || mt == "" || len(contentType) > 128 {
		fail(w, http.StatusBadRequest, CodeInvalid, "asset content type is invalid")
		return
	}
	want := strings.ToLower(strings.TrimSpace(r.Header.Get(HeaderSHA256)))
	if raw, err := hex.DecodeString(want); err != nil || len(raw) != sha256.Size {
		fail(w, http.StatusBadRequest, CodeInvalid, "asset sha256 header is required")
		return
	}
	if r.ContentLength < 0 {
		fail(w, http.StatusBadRequest, CodeInvalid, "asset upload requires Content-Length")
		return
	}
	if r.ContentLength > a.maxBytes() {
		fail(w, http.StatusRequestEntityTooLarge, CodeTooLarge, "asset exceeds the staging size cap")
		return
	}
	// ponytail: check-then-put is not atomic; a racing duplicate loses at the
	// store (last writer wins). The token already bounds who may write this key.
	if rc, _, err := a.Artifacts.GetArtifact(r.Context(), jobID, key); err == nil {
		_ = rc.Close()
		fail(w, http.StatusConflict, CodeExists, "asset already exists")
		return
	} else if !artifacts.IsNotFound(err) {
		fail(w, http.StatusInternalServerError, CodeInternal, "asset service unavailable")
		return
	}
	body := &hashingReader{r: http.MaxBytesReader(w, r.Body, a.maxBytes()), h: sha256.New()}
	rec, err := a.Artifacts.PutArtifact(r.Context(), jobID, key, contentType, body, r.ContentLength)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusRequestEntityTooLarge, CodeTooLarge, "asset exceeds the staging size cap")
			return
		}
		fail(w, http.StatusInternalServerError, CodeInternal, "failed to store asset")
		return
	}
	got := hex.EncodeToString(body.h.Sum(nil))
	if body.n != r.ContentLength || got != want || (rec.Checksum != "" && !strings.EqualFold(rec.Checksum, got)) {
		// Fail closed: nothing that failed verification stays readable.
		_ = a.Artifacts.DeleteArtifact(context.WithoutCancel(r.Context()), jobID, key)
		fail(w, http.StatusUnprocessableEntity, CodeIntegrity, "asset size or sha256 does not match")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "size_bytes": body.n, "sha256": got})
}
