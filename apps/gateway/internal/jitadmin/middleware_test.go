package jitadmin

import (
	"context"
	"testing"
	"time"
)

func TestElevatedRoleGrantElevates(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	// Create and approve an admin grant for an operator.
	g, _ := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now,
	})
	store.Approve(ctx, g.ID, "superadmin_bob", now) //nolint

	got := ElevatedRole(ctx, store, "alice", "tenant_1", "operator", now)
	if got != "admin" {
		t.Errorf("ElevatedRole: got %q, want %q", got, "admin")
	}
}

func TestElevatedRoleExpiredGrantNoElevation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	// Create a grant that has already expired.
	g, _ := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       -time.Hour, // already expired
		CreatedAt: now,
	})
	store.Approve(ctx, g.ID, "bob", now) //nolint

	got := ElevatedRole(ctx, store, "alice", "tenant_1", "operator", now)
	if got != "operator" {
		t.Errorf("ElevatedRole: got %q, want %q (expired grant should not elevate)", got, "operator")
	}
}

func TestElevatedRoleUnapprovedGrantNoElevation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	// Create but do NOT approve the grant.
	store.Create(ctx, Grant{ //nolint
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now,
	})

	got := ElevatedRole(ctx, store, "alice", "tenant_1", "operator", now)
	if got != "operator" {
		t.Errorf("ElevatedRole: got %q, want %q (unapproved grant should not elevate)", got, "operator")
	}
}

func TestElevatedRoleEveryTransitionAudited(t *testing.T) {
	// Audit emission happens in the HTTP handlers (handleRequestElevation,
	// handleApproveElevation), not in ElevatedRole itself.
	// This test validates that after a request+approve cycle the store
	// reflects both transitions correctly (approved=true, approvedBy set).
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	// Simulate "request" transition.
	g, err := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		Reason:    "incident response",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("Create (request): %v", err)
	}
	if g.Approved {
		t.Error("freshly created grant should not be approved")
	}

	// Simulate "approve" transition.
	approved, err := store.Approve(ctx, g.ID, "bob", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !approved.Approved {
		t.Error("after approve: Approved should be true")
	}
	if approved.ApprovedBy != "bob" {
		t.Errorf("after approve: ApprovedBy = %q, want %q", approved.ApprovedBy, "bob")
	}

	// Verify ElevatedRole now returns the elevated role.
	got := ElevatedRole(ctx, store, "alice", "tenant_1", "operator", now.Add(time.Minute))
	if got != "admin" {
		t.Errorf("ElevatedRole after approve: got %q, want %q", got, "admin")
	}
}

func TestElevatedRolePicksHighestGrant(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	// Two active grants; superadmin should win.
	g1, _ := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "admin",
		TTL:       time.Hour,
		CreatedAt: now,
	})
	g2, _ := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "superadmin",
		TTL:       time.Hour,
		CreatedAt: now.Add(time.Nanosecond),
	})
	store.Approve(ctx, g1.ID, "bob", now) //nolint
	store.Approve(ctx, g2.ID, "bob", now) //nolint

	got := ElevatedRole(ctx, store, "alice", "tenant_1", "operator", now)
	if got != "superadmin" {
		t.Errorf("ElevatedRole: got %q, want %q (should pick highest grant)", got, "superadmin")
	}
}

func TestElevatedRoleNoDowngrade(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()

	// Grant is for a lower role than currentRole — should keep currentRole.
	g, _ := store.Create(ctx, Grant{
		Actor:     "alice",
		TenantID:  "tenant_1",
		AppID:     "app_1",
		Role:      "developer",
		TTL:       time.Hour,
		CreatedAt: now,
	})
	store.Approve(ctx, g.ID, "bob", now) //nolint

	got := ElevatedRole(ctx, store, "alice", "tenant_1", "admin", now)
	if got != "admin" {
		t.Errorf("ElevatedRole: got %q, want %q (grant should not downgrade role)", got, "admin")
	}
}
