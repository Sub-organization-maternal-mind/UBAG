// Package attemptcap implements attempt capability tokens (perf-fleet P4.6).
//
// A helper node that holds an attempt lease needs to read inputs and write
// outputs (assets) for that one job. It proves the right to do so with a short
// RS256 token that is:
//
//   - signed with its own keypair, never the app-JWT key (EnsureSeparateKey);
//   - typed (JWT typ "ubag-attempt") and audience-bound (aud "ubag-helper"), so
//     appjwt.Verify rejects it and this package rejects an app JWT;
//   - bound to one node, attempt, lease generation, job and tenant, plus an
//     explicit key set and operation set;
//   - never valid past the attempt lease it was minted for.
//
// A valid token is necessary but not sufficient: Authorize also loads the live
// attempt row, so a fenced, superseded or expired attempt loses access at once
// even though its token has not expired.
//
// The token is a capability, not an identity: the mTLS node identity (helperauth)
// is still required and must equal the token's node.
package attemptcap

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/jobs"
)

const (
	// Type is the JWT typ header of an attempt token.
	Type = "ubag-attempt"
	// Audience is the aud claim of an attempt token.
	Audience = "ubag-helper"
	// MaxTTL bounds a token lifetime; it equals the longest attempt lease.
	MaxTTL = jobs.MaxAttemptLeaseTTL

	OpRead  = "read"
	OpWrite = "write"

	maxTokenLen = 8 << 10
	maxKeys     = 64
	maxKeyLen   = 512
	maxFieldLen = 256
	minKeyBits  = 2048
	headerJSON  = `{"alg":"RS256","typ":"` + Type + `"}`
)

var (
	// ErrInvalid: structural, signature, typ/aud or claim-shape failure.
	ErrInvalid = errors.New("attemptcap: invalid token")
	// ErrExpired: the token is past its exp.
	ErrExpired = errors.New("attemptcap: token is expired")
	// ErrWrongNode: the token was minted for another node.
	ErrWrongNode = errors.New("attemptcap: token is bound to another node")
	// ErrWrongGeneration: the token carries another lease generation.
	ErrWrongGeneration = errors.New("attemptcap: token lease generation mismatch")
	// ErrWrongBinding: attempt, job or tenant differs from the expectation.
	ErrWrongBinding = errors.New("attemptcap: token is bound to another attempt, job or tenant")
	// ErrLeaseExceeded: exp is beyond the lease the token must not outlive.
	ErrLeaseExceeded = errors.New("attemptcap: token outlives the attempt lease")
	// ErrNotAllowed: the requested key or operation is not in the token.
	ErrNotAllowed = errors.New("attemptcap: key or operation not granted by token")
	// ErrFenced: the live attempt row is superseded, inactive or missing.
	ErrFenced = errors.New("attemptcap: attempt is not the live attempt")
)

// Claims is the attempt token payload.
type Claims struct {
	Aud        string   `json:"aud"`
	Node       string   `json:"node"`
	Attempt    string   `json:"attempt"`
	Generation uint64   `json:"generation"`
	Job        string   `json:"job"`
	Tenant     string   `json:"tenant"`
	Keys       []string `json:"keys"`
	Ops        []string `json:"ops"`
	IssuedAt   int64    `json:"iat"`
	Expires    int64    `json:"exp"`
}

// Expect is what the verifier knows from its own state. Every field except Key
// and Op is required (fail closed): a zero value never matches.
type Expect struct {
	Now            time.Time
	Node           string // authenticated mTLS node identity
	Attempt        string
	Generation     uint64
	Job            string
	Tenant         string
	LeaseExpiresAt time.Time // exp must not be beyond this
	Key, Op        string    // when set, must be granted by the token
}

// EnsureSeparateKey refuses an attempt signing key that is too small or equals
// the app-JWT public key, so the two token families can never be confused by
// key reuse.
func EnsureSeparateKey(attemptKey *rsa.PrivateKey, appPublicKey *rsa.PublicKey) error {
	if attemptKey == nil || attemptKey.N.BitLen() < minKeyBits {
		return errors.New("attemptcap: attempt key must be RSA >= 2048 bits")
	}
	if appPublicKey != nil && appPublicKey.Equal(&attemptKey.PublicKey) {
		return errors.New("attemptcap: attempt key must differ from the app JWT key")
	}
	return nil
}

// leaseBoundedExpiry returns min(now+ttl, lease) or an error when ttl is out of
// range or the lease already lapsed.
func leaseBoundedExpiry(att jobs.Attempt, ttl time.Duration, now time.Time) (int64, error) {
	if att.State != jobs.AttemptActive {
		return 0, ErrFenced
	}
	if ttl <= 0 || ttl > MaxTTL {
		return 0, fmt.Errorf("%w: ttl must be in (0, %s]", ErrInvalid, MaxTTL)
	}
	exp := now.Add(ttl)
	if att.LeaseExpiresAt.Before(exp) {
		exp = att.LeaseExpiresAt
	}
	if !exp.After(now) {
		return 0, ErrLeaseExceeded
	}
	return exp.Unix(), nil
}

// Issue mints a token for the live attempt. The expiry is min(now+ttl, lease).
func Issue(att jobs.Attempt, tenant string, keys, ops []string, ttl time.Duration, now time.Time, priv *rsa.PrivateKey) (string, error) {
	if priv == nil {
		return "", errors.New("attemptcap: private key is required")
	}
	exp, err := leaseBoundedExpiry(att, ttl, now)
	if err != nil {
		return "", err
	}
	c := Claims{
		Aud: Audience, Node: att.NodeID, Attempt: att.AttemptID, Generation: att.Generation,
		Job: att.JobID, Tenant: tenant, Keys: keys, Ops: ops,
		IssuedAt: now.Unix(), Expires: exp,
	}
	if err := c.validShape(); err != nil {
		return "", err
	}
	return sign(c, priv)
}

// Renew re-signs a still-valid token with a new exp (min(now+ttl, lease)).
// Every other claim, including iat, is carried over unchanged.
func Renew(token string, pub *rsa.PublicKey, priv *rsa.PrivateKey, att jobs.Attempt, ttl time.Duration, now time.Time) (string, error) {
	if priv == nil {
		return "", errors.New("attemptcap: private key is required")
	}
	c, err := parse(token, pub, now)
	if err != nil {
		return "", err
	}
	if c.Node != att.NodeID || c.Attempt != att.AttemptID || c.Job != att.JobID {
		return "", ErrWrongBinding
	}
	if c.Generation != att.Generation {
		return "", ErrWrongGeneration
	}
	if c.Expires, err = leaseBoundedExpiry(att, ttl, now); err != nil {
		return "", err
	}
	return sign(c, priv)
}

// Verify checks signature, typ/aud, claim shape, expiry and every binding in e.
func Verify(token string, pub *rsa.PublicKey, e Expect) (Claims, error) {
	if e.Now.IsZero() || e.Node == "" || e.Attempt == "" || e.Generation == 0 ||
		e.Job == "" || e.Tenant == "" || e.LeaseExpiresAt.IsZero() {
		return Claims{}, fmt.Errorf("%w: incomplete expectation", ErrInvalid)
	}
	c, err := parse(token, pub, e.Now)
	if err != nil {
		return Claims{}, err
	}
	switch {
	case c.Node != e.Node:
		return Claims{}, ErrWrongNode
	case c.Generation != e.Generation:
		return Claims{}, ErrWrongGeneration
	case c.Attempt != e.Attempt || c.Job != e.Job || c.Tenant != e.Tenant:
		return Claims{}, ErrWrongBinding
	case c.Expires > e.LeaseExpiresAt.Unix():
		return Claims{}, ErrLeaseExceeded
	}
	if (e.Key != "" && !contains(c.Keys, e.Key)) || (e.Op != "" && !contains(c.Ops, e.Op)) {
		return Claims{}, ErrNotAllowed
	}
	return c, nil
}

// Request is what an asset RPC knows when it calls Authorize.
type Request struct {
	Node    string // authenticated mTLS node identity
	Tenant  string // tenant of the job being touched
	Key, Op string
	Now     time.Time
}

// Authorize is the check every asset RPC makes: the token must verify against
// the LIVE attempt row (active, same node/generation, exp within its current
// lease). The job and attempt ids come from the token, but nothing in it is
// trusted until the ledger agrees.
func Authorize(ctx context.Context, store jobs.AttemptStore, token string, pub *rsa.PublicKey, r Request) (Claims, error) {
	c, err := parse(token, pub, r.Now)
	if err != nil {
		return Claims{}, err
	}
	attempts, err := store.ListAttempts(ctx, c.Job)
	if err != nil {
		if errors.Is(err, jobs.ErrAttemptNotFound) {
			return Claims{}, ErrFenced
		}
		return Claims{}, err
	}
	for _, a := range attempts {
		if a.AttemptID != c.Attempt {
			continue
		}
		if a.State != jobs.AttemptActive {
			return Claims{}, ErrFenced
		}
		return Verify(token, pub, Expect{
			Now: r.Now, Node: r.Node, Attempt: a.AttemptID, Generation: a.Generation, Job: a.JobID,
			Tenant: r.Tenant, LeaseExpiresAt: a.LeaseExpiresAt, Key: r.Key, Op: r.Op,
		})
	}
	return Claims{}, ErrFenced
}

func (c Claims) validShape() error {
	bad := func(what string) error { return fmt.Errorf("%w: %s", ErrInvalid, what) }
	if c.Aud != Audience || c.Generation == 0 || c.IssuedAt <= 0 || c.Expires <= 0 {
		return bad("malformed claims")
	}
	for _, f := range [...]string{c.Node, c.Attempt, c.Job, c.Tenant} {
		if f == "" || len(f) > maxFieldLen || f != strings.TrimSpace(f) {
			return bad("malformed claims")
		}
	}
	if !jobs.ValidAttemptID(c.Attempt) {
		return bad("attempt id")
	}
	if len(c.Keys) == 0 || len(c.Keys) > maxKeys || len(c.Ops) == 0 || len(c.Ops) > 2 {
		return bad("key or op set size")
	}
	for _, k := range c.Keys {
		if k == "" || len(k) > maxKeyLen {
			return bad("key set")
		}
	}
	for _, o := range c.Ops {
		if o != OpRead && o != OpWrite {
			return bad("op set")
		}
	}
	return nil
}

// parse verifies everything that needs no ledger state: signature, header,
// payload shape, aud and expiry.
func parse(token string, pub *rsa.PublicKey, now time.Time) (Claims, error) {
	if pub == nil || len(token) > maxTokenLen || now.IsZero() {
		return Claims{}, ErrInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalid
	}
	header, err := b64d(parts[0])
	if err != nil || string(header) != headerJSON {
		return Claims{}, fmt.Errorf("%w: header", ErrInvalid)
	}
	sig, err := b64d(parts[2])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) != nil {
		return Claims{}, fmt.Errorf("%w: signature", ErrInvalid)
	}
	payload, err := b64d(parts[1])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	var c Claims
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Claims{}, fmt.Errorf("%w: claims", ErrInvalid)
	}
	if err := c.validShape(); err != nil {
		return Claims{}, err
	}
	if now.Unix() > c.Expires {
		return Claims{}, ErrExpired
	}
	return c, nil
}

func sign(c Claims, priv *rsa.PrivateKey) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("attemptcap: marshal: %w", err)
	}
	input := b64e([]byte(headerJSON)) + "." + b64e(payload)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("attemptcap: sign: %w", err)
	}
	return input + "." + b64e(sig), nil
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func b64e(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
func b64d(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
