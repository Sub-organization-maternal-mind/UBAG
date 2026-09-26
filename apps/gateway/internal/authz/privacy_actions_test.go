package authz_test

import (
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/authz"
)

// Erasure is GDPR Art. 17: destructive and irreversible. It must not be
// reachable by a principal that only holds read-only export rights. The privacy
// handlers used to authorize BOTH export and erase with "data:export", and
// "data:erase" did not exist in the table at all, so an export-only grant could
// trigger erasure.
func TestEraseIsNotGrantedByExportAlone(t *testing.T) {
	// The action must exist as its own grantable permission...
	if got := authz.Actions("admin"); !contains(got, "data:erase") {
		t.Error("admin should hold data:erase")
	}
	if got := authz.Actions("admin"); !contains(got, "data:export") {
		t.Error("admin should hold data:export")
	}

	// ...and no role below admin may hold it.
	for _, role := range []string{"viewer", "developer", "operator", "service"} {
		if authz.RoleAllows(role, "data:erase") {
			t.Errorf("role %q must not be able to erase data", role)
		}
	}
}

// No non-admin role may hold either privacy action; data:export is admin-only
// today and erase must stay at least as restricted.
func TestPrivacyActionsAreAdminOnly(t *testing.T) {
	for _, role := range []string{"viewer", "developer", "operator", "service"} {
		for _, action := range []string{"data:export", "data:erase"} {
			if authz.RoleAllows(role, action) {
				t.Errorf("role %q unexpectedly holds %s", role, action)
			}
		}
	}
	if !authz.RoleAllows("admin", "data:export") || !authz.RoleAllows("admin", "data:erase") {
		t.Error("admin must hold both privacy actions")
	}
	// superadmin is the documented everything-allowed role.
	if !authz.RoleAllows("superadmin", "data:erase") {
		t.Error("superadmin must hold data:erase")
	}
}

// Guard against reintroducing the shared permission by accident: the two
// actions must remain distinct strings in the table.
func TestExportAndEraseAreDistinctActions(t *testing.T) {
	if "data:export" == "data:erase" {
		t.Fatal("the two privacy actions must be distinct")
	}
	all := authz.Actions("admin")
	if !contains(all, "data:export") || !contains(all, "data:erase") {
		t.Fatalf("admin action list missing a privacy action: %v", all)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
