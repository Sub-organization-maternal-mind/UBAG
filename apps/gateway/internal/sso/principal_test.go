package sso

import (
	"errors"
	"testing"
)

func TestMapPrincipal_FromAttributes(t *testing.T) {
	attrs := map[string][]string{
		"tenant": {"tenant-1"},
		"app":    {"app-1"},
		"role":   {"idp-admin"},
		"sub":    {"operator-123"},
		"email":  {"operator@example.com"},
	}
	mapping := AttributeMapping{
		TenantAttribute:  "tenant",
		AppAttribute:     "app",
		RoleAttribute:    "role",
		SubjectAttribute: "sub",
		EmailAttribute:   "email",
		RoleValues:       map[string]string{"idp-admin": "admin"},
	}
	principal, err := MapPrincipal(attrs, mapping)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal.TenantID != "tenant-1" || principal.AppID != "app-1" {
		t.Errorf("scope = %+v", principal)
	}
	if principal.Role != "admin" {
		t.Errorf("role = %q, want admin", principal.Role)
	}
	if principal.Subject != "operator-123" || principal.Email != "operator@example.com" {
		t.Errorf("identity = %+v", principal)
	}
}

func TestMapPrincipal_DefaultRoleViewer(t *testing.T) {
	attrs := map[string][]string{"tenant": {"t"}, "app": {"a"}}
	principal, err := MapPrincipal(attrs, AttributeMapping{TenantAttribute: "tenant", AppAttribute: "app"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal.Role != "viewer" {
		t.Errorf("role = %q, want viewer", principal.Role)
	}
}

func TestMapPrincipal_StaticFallback(t *testing.T) {
	mapping := AttributeMapping{StaticTenantID: "tenant-static", StaticAppID: "app-static"}
	principal, err := MapPrincipal(map[string][]string{}, mapping)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal.TenantID != "tenant-static" || principal.AppID != "app-static" {
		t.Errorf("static scope = %+v", principal)
	}
}

func TestMapPrincipal_MissingTenant(t *testing.T) {
	_, err := MapPrincipal(map[string][]string{"app": {"a"}}, AttributeMapping{AppAttribute: "app"})
	if !errors.Is(err, ErrTenantUnresolved) {
		t.Fatalf("expected ErrTenantUnresolved, got %v", err)
	}
}

func TestMapPrincipal_MissingApp(t *testing.T) {
	_, err := MapPrincipal(map[string][]string{"tenant": {"t"}}, AttributeMapping{TenantAttribute: "tenant"})
	if !errors.Is(err, ErrAppUnresolved) {
		t.Fatalf("expected ErrAppUnresolved, got %v", err)
	}
}

func TestMapPrincipal_FromClaims(t *testing.T) {
	claims := Claims{Raw: map[string]any{
		"tenant": "tenant-1",
		"app":    "app-1",
		"roles":  []any{"admin"},
		"sub":    "operator-123",
	}}
	mapping := AttributeMapping{
		TenantAttribute:  "tenant",
		AppAttribute:     "app",
		RoleAttribute:    "roles",
		SubjectAttribute: "sub",
	}
	principal, err := MapPrincipal(claims.Attributes(), mapping)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal.Role != "admin" || principal.Subject != "operator-123" {
		t.Errorf("principal = %+v", principal)
	}
}
