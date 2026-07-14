package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
)

// recordingAuditStore embeds a real memory store and records the (action,
// outcome) of every Append so tests can assert which authorization decisions
// were audited.
type recordingAuditStore struct {
	audit.Store
	mu      sync.Mutex
	entries []string
}

func (r *recordingAuditStore) Append(ctx context.Context, rec audit.Record) (audit.Record, error) {
	r.mu.Lock()
	r.entries = append(r.entries, rec.Action+"/"+rec.Outcome)
	r.mu.Unlock()
	return r.Store.Append(ctx, rec)
}

// TestEmitAuthorizationAuditSkipsAllowedReads verifies the hot-path fix: an
// allowed read authorization (e.g. job:read status polling) is NOT written to
// the audit chain, while denials and all mutations still are.
func TestEmitAuthorizationAuditSkipsAllowedReads(t *testing.T) {
	rec := &recordingAuditStore{Store: audit.NewMemoryStore()}
	s := NewServer(Config{AppSecret: "dev-secret", Audit: rec})
	p := authenticatedPrincipal{Role: "service", TenantID: "t", AppID: "a", Subject: "svc"}
	r := httptest.NewRequest(http.MethodGet, "/v1/jobs/job_1", nil)

	// Skipped: allowed reads.
	s.emitAuthorizationAudit(r, p, "job:read", "allow")
	s.emitAuthorizationAudit(r, p, "browser:read", "allow")
	s.emitAuthorizationAudit(r, p, "concurrency:read", "allow")

	// Recorded: a denied read (security-relevant) and an allowed mutation.
	s.emitAuthorizationAudit(r, p, "job:read", "deny")
	s.emitAuthorizationAudit(r, p, "job:create", "allow")
	s.emitAuthorizationAudit(r, p, "job:cancel", "allow")

	want := []string{
		"authorize:job:read/deny",
		"authorize:job:create/allow",
		"authorize:job:cancel/allow",
	}
	rec.mu.Lock()
	got := rec.entries
	rec.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("expected %d audit records, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("audit record %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}
