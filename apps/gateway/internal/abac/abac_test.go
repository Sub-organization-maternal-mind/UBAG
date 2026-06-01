package abac

import (
	"testing"
)

func TestDefaultEnforcerAllowsAll(t *testing.T) {
	e := DefaultEnforcer()
	ok, err := e.Allow(Principal{Role: "viewer"}, "jobs", "job:read")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !ok {
		t.Error("default enforcer must allow all actions")
	}
}

func TestNilEnforcerAllowsAll(t *testing.T) {
	var e *Enforcer
	ok, err := e.Allow(Principal{Role: "viewer"}, "jobs", "job:read")
	if err != nil {
		t.Fatalf("Allow on nil enforcer: %v", err)
	}
	if !ok {
		t.Error("nil enforcer must allow all actions")
	}
}

func TestRuleAllow(t *testing.T) {
	bundle := PolicyBundle{Rules: []Rule{
		{Name: "admin-only-delete", Condition: `principal["role"] == "admin" || action != "data:delete"`},
	}}
	e, err := NewEnforcer(bundle)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// admin can do anything
	ok, _ := e.Allow(Principal{Role: "admin"}, "jobs", "data:delete")
	if !ok {
		t.Error("admin should be allowed data:delete")
	}

	// viewer cannot delete
	ok, _ = e.Allow(Principal{Role: "viewer"}, "jobs", "data:delete")
	if ok {
		t.Error("viewer should be denied data:delete")
	}

	// viewer can read
	ok, _ = e.Allow(Principal{Role: "viewer"}, "jobs", "job:read")
	if !ok {
		t.Error("viewer should be allowed job:read by this rule")
	}
}

func TestMultipleRulesAND(t *testing.T) {
	bundle := PolicyBundle{Rules: []Rule{
		{Name: "tenant-gate", Condition: `principal["tenant_id"] == "tenant_prod"`},
		{Name: "role-gate", Condition: `principal["role"] == "admin"`},
	}}
	e, _ := NewEnforcer(bundle)

	// Both conditions met
	ok, _ := e.Allow(Principal{TenantID: "tenant_prod", Role: "admin"}, "jobs", "job:create")
	if !ok {
		t.Error("should be allowed when both rules pass")
	}

	// Only tenant matches
	ok, _ = e.Allow(Principal{TenantID: "tenant_prod", Role: "viewer"}, "jobs", "job:create")
	if ok {
		t.Error("should be denied when role rule fails")
	}

	// Only role matches
	ok, _ = e.Allow(Principal{TenantID: "other_tenant", Role: "admin"}, "jobs", "job:create")
	if ok {
		t.Error("should be denied when tenant rule fails")
	}
}

func TestNewEnforcerRejectsBadCEL(t *testing.T) {
	bundle := PolicyBundle{Rules: []Rule{
		{Name: "bad", Condition: `invalid ~~~ syntax`},
	}}
	if _, err := NewEnforcer(bundle); err == nil {
		t.Error("NewEnforcer must reject invalid CEL syntax")
	}
}

func TestNewEnforcerRejectsNonBoolCEL(t *testing.T) {
	bundle := PolicyBundle{Rules: []Rule{
		{Name: "non-bool", Condition: `"just a string"`},
	}}
	e, err := NewEnforcer(bundle)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	// Allow should fail on eval because the expression returns string, not bool
	_, err = e.Allow(Principal{}, "r", "a")
	if err == nil {
		t.Error("Allow must return error for non-bool expression")
	}
}

func TestLoadBundleFromFileMissing(t *testing.T) {
	if _, err := LoadBundleFromFile("/nonexistent/path.json"); err == nil {
		t.Error("LoadBundleFromFile must fail for missing file")
	}
}
