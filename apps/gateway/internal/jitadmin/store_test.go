package jitadmin

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreCreateAndGet(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	input := Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		Reason:    "incident response",
		TTL:       time.Hour,
		CreatedAt: now,
	}

	created, err := store.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create: expected non-empty ID")
	}
	if len(created.ID) < 4 || created.ID[:4] != "jit_" {
		t.Errorf("Create: expected ID to start with 'jit_', got %q", created.ID)
	}
	if created.ExpiresAt.IsZero() {
		t.Fatal("Create: expected non-zero ExpiresAt")
	}
	wantExpiry := now.Add(time.Hour)
	if diff := created.ExpiresAt.Sub(wantExpiry); diff < -time.Second || diff > time.Second {
		t.Errorf("ExpiresAt = %v, want ~%v", created.ExpiresAt, wantExpiry)
	}

	got, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("Get: ID mismatch: got %q, want %q", got.ID, created.ID)
	}

	_, err = store.Get(ctx, "nonexistent")
	if !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("Get nonexistent: expected ErrGrantNotFound, got %v", err)
	}
}

func TestMemoryStoreApprove(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	g, err := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	approveTime := now.Add(time.Minute)
	approved, err := store.Approve(ctx, g.ID, "bob", approveTime)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !approved.Approved {
		t.Error("Approve: Approved should be true")
	}
	if approved.ApprovedBy != "bob" {
		t.Errorf("Approve: ApprovedBy = %q, want %q", approved.ApprovedBy, "bob")
	}
	if approved.ApprovedAt == nil || !approved.ApprovedAt.Equal(approveTime) {
		t.Errorf("Approve: ApprovedAt = %v, want %v", approved.ApprovedAt, approveTime)
	}

	// Approving non-existent grant returns ErrGrantNotFound.
	_, err = store.Approve(ctx, "nonexistent", "bob", approveTime)
	if !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("Approve nonexistent: expected ErrGrantNotFound, got %v", err)
	}
}

func TestMemoryStoreActiveGrants(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	// Create an approved, active grant.
	g1, _ := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now,
	})
	store.Approve(ctx, g1.ID, "bob", now) //nolint

	// Create an unapproved grant — should NOT appear in ActiveGrants.
	store.Create(ctx, Grant{ //nolint
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now.Add(time.Nanosecond), // unique timestamp
	})

	// Create an expired grant (negative TTL).
	g3, _ := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       -time.Hour,
		CreatedAt: now.Add(2 * time.Nanosecond),
	})
	store.Approve(ctx, g3.ID, "bob", now) //nolint

	// Create a revoked grant.
	g4, _ := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now.Add(3 * time.Nanosecond),
	})
	store.Approve(ctx, g4.ID, "bob", now) //nolint
	store.Revoke(ctx, g4.ID)              //nolint

	// Create a grant for a different tenant — should NOT appear.
	g5, _ := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_2",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now.Add(4 * time.Nanosecond),
	})
	store.Approve(ctx, g5.ID, "bob", now) //nolint

	active, err := store.ActiveGrants(ctx, "alice", "tenant_1", now)
	if err != nil {
		t.Fatalf("ActiveGrants: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("ActiveGrants: expected 1 active grant, got %d", len(active))
	}
	if len(active) > 0 && active[0].ID != g1.ID {
		t.Errorf("ActiveGrants: expected grant %q, got %q", g1.ID, active[0].ID)
	}
}

func TestMemoryStoreRevoke(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	g, err := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	store.Approve(ctx, g.ID, "bob", now) //nolint

	if err := store.Revoke(ctx, g.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, _ := store.Get(ctx, g.ID)
	if !got.Revoked {
		t.Error("Revoke: Revoked should be true")
	}

	// Revoking non-existent grant returns ErrGrantNotFound.
	if err := store.Revoke(ctx, "nonexistent"); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("Revoke nonexistent: expected ErrGrantNotFound, got %v", err)
	}
}
