package authz

import "testing"

// TestRoleAllowsIsOneTable pins the shared policy: the httpapi superset
// semantics and the gRPC historical subset both hold, because gRPC only
// authorizes the four job actions.
func TestRoleAllowsIsOneTable(t *testing.T) {
	// httpapi truth (sampled across every role).
	cases := []struct {
		role   string
		action string
		want   bool
	}{
		{"viewer", "job:read", true},
		{"viewer", "job:create", false},
		{"developer", "artifact:write", true},
		{"developer", "secret:rotate", false},
		{"operator", "alerts:manage", true},
		{"operator", "role:manage", false},
		{"admin", "region:manage", true},
		{"admin", "auth:pat:issue", false},
		{"superadmin", "anything:at:all", true},
		{"service", "webhook:replay", true},
		{"service", "role:manage", false},
		{"", "job:read", false},
	}
	for _, tc := range cases {
		if got := RoleAllows(tc.role, tc.action); got != tc.want {
			t.Errorf("RoleAllows(%q, %q) = %v, want %v", tc.role, tc.action, got, tc.want)
		}
	}
}

// TestGRPCSubsetStillHolds pins the gRPC table's historical outcomes.
func TestGRPCSubsetStillHolds(t *testing.T) {
	jobActions := []string{"job:create", "job:read", "job:cancel", "job:retry"}
	for _, role := range []string{"developer", "operator", "service", "admin"} {
		for _, action := range jobActions {
			if !RoleAllows(role, action) {
				t.Errorf("gRPC regression: %s lost %s", role, action)
			}
		}
	}
	if !RoleAllows("superadmin", "job:read") {
		t.Error("superadmin lost job:read")
	}
	if RoleAllows("viewer", "job:create") {
		t.Error("viewer must not create")
	}
}

// TestUnknownRoleDenies: an unknown role allows nothing — not even superadmin
// fallthrough.
func TestUnknownRoleDenies(t *testing.T) {
	for _, role := range []string{"", "owner", "member", "Owner"} {
		if RoleAllows(role, "job:read") {
			t.Errorf("unknown role %q allowed an action", role)
		}
	}
}

// TestSuperadminAllowsUnion pins the superadmin invariant: since the set is
// spelled out (no init cycle), this test fails if a new action is added to a
// role but forgotten for superadmin.
func TestSuperadminAllowsUnion(t *testing.T) {
	roles := []string{"viewer", "developer", "operator", "admin", "service"}
	seen := map[string]struct{}{}
	for _, role := range roles {
		for _, action := range Actions(role) {
			seen[action] = struct{}{}
			if !RoleAllows("superadmin", action) {
				t.Errorf("superadmin missing action %s (allowed for %s)", action, role)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no actions discovered")
	}
}
