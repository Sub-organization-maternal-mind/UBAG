package pat

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIssueResolveRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	token, err := Issue("tenant1", "app1", "admin", time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasPrefix(token.ID, tokenPrefix) {
		t.Errorf("ID = %q, want %s prefix", token.ID, tokenPrefix)
	}
	if token.TenantID != "tenant1" || token.AppID != "app1" || token.Role != "admin" {
		t.Errorf("token fields wrong: %+v", token)
	}

	if err := store.Save(ctx, token); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := store.Resolve(ctx, token.ID, time.Now())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("Resolve: token not found")
	}
	if got.TenantID != "tenant1" {
		t.Errorf("resolved TenantID = %q, want tenant1", got.TenantID)
	}
}

func TestIssueWithoutExpiry(t *testing.T) {
	token, err := Issue("t", "a", "viewer", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token.IsExpired(time.Now().Add(365 * 24 * time.Hour)) {
		t.Error("zero-ttl token should never expire")
	}
}

func TestTokenExpiry(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	token, _ := Issue("t", "a", "viewer", time.Millisecond)
	_ = store.Save(ctx, token)

	time.Sleep(5 * time.Millisecond)
	_, ok, _ := store.Resolve(ctx, token.ID, time.Now())
	if ok {
		t.Error("expired token should not resolve")
	}
}

func TestResolveUnknownToken(t *testing.T) {
	store := NewMemoryStore()
	_, ok, _ := store.Resolve(context.Background(), "ubag_pat_notexist", time.Now())
	if ok {
		t.Error("non-existent token should not resolve")
	}
}

func TestRevoke(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	token, _ := Issue("t", "a", "admin", time.Hour)
	_ = store.Save(ctx, token)

	if err := store.Revoke(ctx, token.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, ok, _ := store.Resolve(ctx, token.ID, time.Now())
	if ok {
		t.Error("revoked token should not resolve")
	}
}

func TestIsValidFormat(t *testing.T) {
	token, _ := Issue("t", "a", "admin", time.Hour)
	if !IsValidFormat(token.ID) {
		t.Errorf("IsValidFormat(%q) = false", token.ID)
	}
	if IsValidFormat("Bearer abc123") {
		t.Error("IsValidFormat should reject non-PAT")
	}
	if IsValidFormat(tokenPrefix) {
		t.Error("IsValidFormat should reject bare prefix")
	}
}

func TestIssueRequiresTenantAndApp(t *testing.T) {
	if _, err := Issue("", "app", "admin", 0); err == nil {
		t.Error("Issue must fail for empty tenantID")
	}
	if _, err := Issue("tenant", "", "admin", 0); err == nil {
		t.Error("Issue must fail for empty appID")
	}
}

func TestTokensAreUnique(t *testing.T) {
	t1, _ := Issue("t", "a", "viewer", 0)
	t2, _ := Issue("t", "a", "viewer", 0)
	if t1.ID == t2.ID {
		t.Error("two issued tokens must be distinct")
	}
}
