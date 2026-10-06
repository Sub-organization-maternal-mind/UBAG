package attemptcap

import (
	"context"
	"crypto/rsa"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/appjwt"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
)

var (
	keyOnce         sync.Once
	capKey, appKey_ *rsa.PrivateKey
)

func keys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		if capKey, err = appjwt.GenerateKeyPair(); err != nil {
			panic(err)
		}
		if appKey_, err = appjwt.GenerateKeyPair(); err != nil {
			panic(err)
		}
	})
	return capKey, appKey_
}

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func attempt(lease time.Duration) jobs.Attempt {
	return jobs.Attempt{
		JobID: "job_1", AttemptID: "att_abc", Generation: 3, NodeID: "helper-1",
		State: jobs.AttemptActive, LeaseExpiresAt: now.Add(lease),
	}
}

func expect(att jobs.Attempt) Expect {
	return Expect{
		Now: now, Node: att.NodeID, Attempt: att.AttemptID, Generation: att.Generation,
		Job: att.JobID, Tenant: "tenant_a", LeaseExpiresAt: att.LeaseExpiresAt,
	}
}

func issue(t *testing.T, att jobs.Attempt, ttl time.Duration) string {
	t.Helper()
	priv, _ := keys(t)
	tok, err := Issue(att, "tenant_a", []string{"inputs/a", "outputs/b"}, []string{OpRead, OpWrite}, ttl, now, priv)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return tok
}

func TestRoundTripAndExpBoundedByLease(t *testing.T) {
	priv, _ := keys(t)
	att := attempt(2 * time.Minute)
	tok := issue(t, att, 10*time.Minute) // ttl longer than lease
	c, err := Verify(tok, &priv.PublicKey, expect(att))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.Aud != Audience || c.Expires != att.LeaseExpiresAt.Unix() {
		t.Fatalf("exp must be clamped to the lease: %+v", c)
	}
}

func TestVerifyRejections(t *testing.T) {
	priv, other := keys(t)
	att := attempt(2 * time.Minute)
	tok := issue(t, att, time.Minute)
	pub := &priv.PublicKey

	cases := map[string]struct {
		mutate func(*Expect)
		pub    *rsa.PublicKey
		want   error
	}{
		"expired":          {func(e *Expect) { e.Now = now.Add(2 * time.Minute) }, pub, ErrExpired},
		"wrong node":       {func(e *Expect) { e.Node = "helper-2" }, pub, ErrWrongNode},
		"wrong generation": {func(e *Expect) { e.Generation = 4 }, pub, ErrWrongGeneration},
		"wrong attempt":    {func(e *Expect) { e.Attempt = "att_zzz" }, pub, ErrWrongBinding},
		"wrong job":        {func(e *Expect) { e.Job = "job_2" }, pub, ErrWrongBinding},
		"wrong tenant":     {func(e *Expect) { e.Tenant = "tenant_b" }, pub, ErrWrongBinding},
		"exp beyond lease": {func(e *Expect) { e.LeaseExpiresAt = now.Add(30 * time.Second) }, pub, ErrLeaseExceeded},
		"wrong key set":    {func(e *Expect) { e.Key = "inputs/other" }, pub, ErrNotAllowed},
		"wrong op":         {func(e *Expect) { e.Op = "delete" }, pub, ErrNotAllowed},
		"wrong signer key": {func(e *Expect) {}, &other.PublicKey, ErrInvalid},
		"incomplete":       {func(e *Expect) { e.Node = "" }, pub, ErrInvalid},
	}
	for name, tc := range cases {
		e := expect(att)
		tc.mutate(&e)
		if _, err := Verify(tok, tc.pub, e); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v want %v", name, err, tc.want)
		}
	}
	e := expect(att)
	e.Key, e.Op = "outputs/b", OpWrite
	if _, err := Verify(tok, pub, e); err != nil {
		t.Errorf("granted key/op rejected: %v", err)
	}
}

func TestIssueRefusals(t *testing.T) {
	priv, _ := keys(t)
	ok := []string{"k"}
	ops := []string{OpRead}
	fin := attempt(time.Minute)
	fin.State = jobs.AttemptFinished
	lapsed := attempt(-time.Second)
	for name, run := range map[string]func() error{
		"inactive attempt": func() error { _, e := Issue(fin, "t", ok, ops, time.Minute, now, priv); return e },
		"lapsed lease":     func() error { _, e := Issue(lapsed, "t", ok, ops, time.Minute, now, priv); return e },
		"ttl zero":         func() error { _, e := Issue(attempt(time.Minute), "t", ok, ops, 0, now, priv); return e },
		"ttl too long":     func() error { _, e := Issue(attempt(time.Hour), "t", ok, ops, MaxTTL+time.Second, now, priv); return e },
		"no keys":          func() error { _, e := Issue(attempt(time.Minute), "t", nil, ops, time.Minute, now, priv); return e },
		"bad op": func() error {
			_, e := Issue(attempt(time.Minute), "t", ok, []string{"x"}, time.Minute, now, priv)
			return e
		},
		"empty tenant": func() error { _, e := Issue(attempt(time.Minute), "", ok, ops, time.Minute, now, priv); return e },
	} {
		if run() == nil {
			t.Errorf("%s: Issue must fail", name)
		}
	}
}

func TestAppJWTAndAttemptTokenAreMutuallyRejected(t *testing.T) {
	priv, other := keys(t)
	att := attempt(time.Minute)
	tok := issue(t, att, time.Minute)

	// attempt token -> app verifier, even when signed with the app key itself.
	if _, err := appjwt.Verify(tok, &priv.PublicKey); err == nil {
		t.Fatal("appjwt.Verify must reject an attempt token (typ/aud) even with the right key")
	}
	if _, err := appjwt.Verify(tok, &other.PublicKey); err == nil {
		t.Fatal("appjwt.Verify must reject an attempt token under the app key")
	}
	// app token -> attempt verifier, including one signed by the attempt key.
	for _, k := range []*rsa.PrivateKey{priv, other} {
		app, err := appjwt.IssueToken("tenant_a", "app_a", "service", time.Minute, k)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(app, &k.PublicKey, expect(att)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("attempt verifier must reject an app JWT: %v", err)
		}
	}
}

func TestUnknownClaimsAndTamperRejected(t *testing.T) {
	priv, _ := keys(t)
	att := attempt(time.Minute)
	tok := issue(t, att, time.Minute)
	parts := strings.Split(tok, ".")
	// flip a payload byte -> signature fails
	tampered := parts[0] + "." + b64e([]byte(`{"aud":"ubag-helper"}`)) + "." + parts[2]
	if _, err := Verify(tampered, &priv.PublicKey, expect(att)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered payload accepted: %v", err)
	}
	// alg=none style header
	none := b64e([]byte(`{"alg":"none","typ":"ubag-attempt"}`)) + "." + parts[1] + "."
	if _, err := Verify(none, &priv.PublicKey, expect(att)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("alg none accepted: %v", err)
	}
}

func TestRenewKeepsClaimsExceptExp(t *testing.T) {
	priv, _ := keys(t)
	pub := &priv.PublicKey
	att := attempt(time.Minute)
	tok := issue(t, att, 30*time.Second)
	before, _ := parse(tok, pub, now)

	att.LeaseExpiresAt = now.Add(5 * time.Minute) // lease renewed
	later := now.Add(20 * time.Second)
	renewed, err := Renew(tok, pub, priv, att, time.Minute, later)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	after, err := parse(renewed, pub, later)
	if err != nil {
		t.Fatal(err)
	}
	if after.Expires != later.Add(time.Minute).Unix() || after.Expires <= before.Expires {
		t.Fatalf("exp not extended: %d -> %d", before.Expires, after.Expires)
	}
	after.Expires = before.Expires
	if after.Node != before.Node || after.Attempt != before.Attempt || after.Generation != before.Generation ||
		after.Job != before.Job || after.Tenant != before.Tenant || after.IssuedAt != before.IssuedAt ||
		strings.Join(after.Keys, ",") != strings.Join(before.Keys, ",") || strings.Join(after.Ops, ",") != strings.Join(before.Ops, ",") {
		t.Fatalf("renew changed non-exp claims: %+v vs %+v", before, after)
	}

	stale := att
	stale.Generation = 4
	if _, err := Renew(tok, pub, priv, stale, time.Minute, later); !errors.Is(err, ErrWrongGeneration) {
		t.Errorf("renew against newer generation: %v", err)
	}
	if _, err := Renew(tok, pub, priv, att, time.Minute, now.Add(time.Hour)); !errors.Is(err, ErrExpired) {
		t.Errorf("renew of an expired token: %v", err)
	}
}

func TestEnsureSeparateKey(t *testing.T) {
	priv, other := keys(t)
	if err := EnsureSeparateKey(priv, &other.PublicKey); err != nil {
		t.Fatalf("distinct keys rejected: %v", err)
	}
	if err := EnsureSeparateKey(priv, &priv.PublicKey); err == nil {
		t.Fatal("shared key must be rejected")
	}
	if err := EnsureSeparateKey(nil, nil); err == nil {
		t.Fatal("nil key must be rejected")
	}
}

func TestAuthorizeChecksLiveAttemptRow(t *testing.T) {
	ctx := context.Background()
	priv, _ := keys(t)
	pub := &priv.PublicKey
	store := jobs.NewMemoryStore()
	job, err := store.Create(ctx, jobs.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", Target: "mock",
		CommandType: "submit", Input: map[string]any{"prompt": "x"}, TraceID: "trace_cap",
	})
	if err != nil {
		t.Fatal(err)
	}
	a1, err := store.BeginAttempt(ctx, jobs.BeginAttemptRequest{
		JobID: job.ID, AttemptID: "att_one", NodeID: "helper-1", TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}
	clock := time.Now()
	tok, err := Issue(a1, "tenant_a", []string{"k"}, []string{OpRead}, time.Minute, clock, priv)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Node: "helper-1", Tenant: "tenant_a", Key: "k", Op: OpRead, Now: clock}
	if _, err := Authorize(ctx, store, tok, pub, req); err != nil {
		t.Fatalf("live attempt must authorize: %v", err)
	}
	wrongNode := req
	wrongNode.Node = "helper-2"
	if _, err := Authorize(ctx, store, tok, pub, wrongNode); !errors.Is(err, ErrWrongNode) {
		t.Errorf("wrong node: %v", err)
	}
	wrongTenant := req
	wrongTenant.Tenant = "tenant_b"
	if _, err := Authorize(ctx, store, tok, pub, wrongTenant); !errors.Is(err, ErrWrongBinding) {
		t.Errorf("wrong tenant: %v", err)
	}

	// A token for an attempt the ledger never recorded (forged job/attempt ids
	// signed with a stolen key) is fenced.
	ghost := a1
	ghost.AttemptID = "att_ghost"
	gt, _ := Issue(ghost, "tenant_a", []string{"k"}, []string{OpRead}, time.Minute, clock, priv)
	if _, err := Authorize(ctx, store, gt, pub, req); !errors.Is(err, ErrFenced) {
		t.Errorf("unknown attempt: %v", err)
	}

	// Supersede: once a successor takes over a lapsed lease, the predecessor's
	// still-unexpired token is dead (the fence is the ledger, not exp).
	job2, err := store.Create(ctx, jobs.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", Target: "mock",
		CommandType: "submit", Input: map[string]any{"prompt": "y"}, TraceID: "trace_cap2",
	})
	if err != nil {
		t.Fatal(err)
	}
	b1, err := store.BeginAttempt(ctx, jobs.BeginAttemptRequest{
		JobID: job2.ID, AttemptID: "att_b1", NodeID: "helper-1", TTL: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	bt, err := Issue(b1, "tenant_a", []string{"k"}, []string{OpRead}, time.Minute, clock, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Authorize(ctx, store, bt, pub, req); err != nil {
		t.Fatalf("live b1: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := store.BeginAttempt(ctx, jobs.BeginAttemptRequest{
		JobID: job2.ID, AttemptID: "att_b2", NodeID: "helper-2", ExpectedGeneration: 1, TTL: time.Minute,
	}); err != nil {
		t.Fatalf("successor: %v", err)
	}
	if _, err := Authorize(ctx, store, bt, pub, req); !errors.Is(err, ErrFenced) {
		t.Fatalf("superseded attempt token must be fenced: %v", err)
	}
}
