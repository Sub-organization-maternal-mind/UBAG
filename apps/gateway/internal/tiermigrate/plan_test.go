package tiermigrate_test

import (
	"errors"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/profile"
	"github.com/ubag/ubag/apps/gateway/internal/tiermigrate"
)

// hasStep reports whether the plan contains at least one step of the given kind.
func hasStep(plan *tiermigrate.MigrationPlan, kind tiermigrate.StepKind) bool {
	for _, s := range plan.Steps {
		if s.Kind == kind {
			return true
		}
	}
	return false
}

func TestPlan_EdgeToSmall(t *testing.T) {
	plan, err := tiermigrate.Plan(profile.Edge, profile.Small)
	if err != nil {
		t.Fatalf("Plan(edge, small) returned unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("Plan returned nil plan")
	}
	if plan.FromProfile != profile.Edge {
		t.Errorf("FromProfile = %q, want %q", plan.FromProfile, profile.Edge)
	}
	if plan.ToProfile != profile.Small {
		t.Errorf("ToProfile = %q, want %q", plan.ToProfile, profile.Small)
	}

	// Edge (SQLite) → Small (Postgres) must trigger a DB migration step
	if !hasStep(plan, tiermigrate.StepMigrateDB) {
		t.Error("expected StepMigrateDB in edge→small plan")
	}
	// SemanticCache: Optional → On
	if !hasStep(plan, tiermigrate.StepEnableCache) {
		t.Error("expected StepEnableCache in edge→small plan")
	}
	// MultiTenantRBAC: Off → On
	if !hasStep(plan, tiermigrate.StepEnableRBAC) {
		t.Error("expected StepEnableRBAC in edge→small plan")
	}
	// AuditDelivery: none → local-file
	if !hasStep(plan, tiermigrate.StepSwitchAudit) {
		t.Error("expected StepSwitchAudit in edge→small plan")
	}
	// BrowserSessionPoolMax: 3 → 30
	if !hasStep(plan, tiermigrate.StepUpgradeBrowser) {
		t.Error("expected StepUpgradeBrowser in edge→small plan")
	}

	if len(plan.Steps) == 0 {
		t.Error("expected at least one step in edge→small plan")
	}
}

func TestPlan_SmallToStandard(t *testing.T) {
	plan, err := tiermigrate.Plan(profile.Small, profile.Standard)
	if err != nil {
		t.Fatalf("Plan(small, standard) returned unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("Plan returned nil plan")
	}

	// JobBackend: postgres → postgres-ha
	if !hasStep(plan, tiermigrate.StepMigrateDB) {
		t.Error("expected StepMigrateDB in small→standard plan")
	}
	// SSO: Off → On
	if !hasStep(plan, tiermigrate.StepEnableSSO) {
		t.Error("expected StepEnableSSO in small→standard plan")
	}
	// AuditDelivery: local-file → siem
	if !hasStep(plan, tiermigrate.StepSwitchAudit) {
		t.Error("expected StepSwitchAudit in small→standard plan")
	}
	// SCIM: Off → Optional
	if !hasStep(plan, tiermigrate.StepEnableSCIM) {
		t.Error("expected StepEnableSCIM in small→standard plan")
	}
	// BrowserSessionPool: 30 → unlimited
	if !hasStep(plan, tiermigrate.StepUpgradeBrowser) {
		t.Error("expected StepUpgradeBrowser in small→standard plan")
	}
}

func TestPlan_StandardToEnterprise(t *testing.T) {
	plan, err := tiermigrate.Plan(profile.Standard, profile.Enterprise)
	if err != nil {
		t.Fatalf("Plan(standard, enterprise) returned unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("Plan returned nil plan")
	}

	// JobBackend: postgres-ha → multi-region
	if !hasStep(plan, tiermigrate.StepMigrateDB) {
		t.Error("expected StepMigrateDB in standard→enterprise plan")
	}
	// SCIM: Optional → On
	if !hasStep(plan, tiermigrate.StepEnableSCIM) {
		t.Error("expected StepEnableSCIM in standard→enterprise plan")
	}
	// AuditDelivery: siem → siem-immutable
	if !hasStep(plan, tiermigrate.StepSwitchAudit) {
		t.Error("expected StepSwitchAudit in standard→enterprise plan")
	}
	// GeoReplication: Optional → On
	if !hasStep(plan, tiermigrate.StepEnableGeoRepl) {
		t.Error("expected StepEnableGeoRepl in standard→enterprise plan")
	}
}

func TestPlan_NonAdjacent_EdgeToEnterprise(t *testing.T) {
	planFull, err := tiermigrate.Plan(profile.Edge, profile.Enterprise)
	if err != nil {
		t.Fatalf("Plan(edge, enterprise) returned unexpected error: %v", err)
	}
	if planFull == nil {
		t.Fatal("Plan returned nil plan")
	}

	// Compute each adjacent-step count independently for comparison
	planES, errES := tiermigrate.Plan(profile.Edge, profile.Small)
	if errES != nil {
		t.Fatalf("Plan(edge, small): %v", errES)
	}
	planSSt, errSSt := tiermigrate.Plan(profile.Small, profile.Standard)
	if errSSt != nil {
		t.Fatalf("Plan(small, standard): %v", errSSt)
	}
	planStE, errStE := tiermigrate.Plan(profile.Standard, profile.Enterprise)
	if errStE != nil {
		t.Fatalf("Plan(standard, enterprise): %v", errStE)
	}

	minExpected := len(planES.Steps) + len(planSSt.Steps) + len(planStE.Steps)
	if len(planFull.Steps) < minExpected {
		t.Errorf("edge→enterprise has %d steps, want >= %d (sum of adjacent plans)", len(planFull.Steps), minExpected)
	}

	// Must include all step kinds that appear in any adjacent plan
	for _, kind := range []tiermigrate.StepKind{
		tiermigrate.StepMigrateDB,
		tiermigrate.StepEnableCache,
		tiermigrate.StepEnableRBAC,
		tiermigrate.StepEnableSSO,
		tiermigrate.StepEnableSCIM,
		tiermigrate.StepSwitchAudit,
		tiermigrate.StepEnableGeoRepl,
		tiermigrate.StepUpgradeBrowser,
	} {
		if !hasStep(planFull, kind) {
			t.Errorf("edge→enterprise plan missing step kind %q", kind)
		}
	}
}

func TestPlan_Downgrade_Rejected(t *testing.T) {
	_, err := tiermigrate.Plan(profile.Small, profile.Edge)
	if !errors.Is(err, tiermigrate.ErrDowngradeUnsupported) {
		t.Errorf("expected ErrDowngradeUnsupported, got %v", err)
	}
}

func TestPlan_SameProfile_Rejected(t *testing.T) {
	_, err := tiermigrate.Plan(profile.Small, profile.Small)
	if !errors.Is(err, tiermigrate.ErrDowngradeUnsupported) {
		t.Errorf("expected ErrDowngradeUnsupported for same profile, got %v", err)
	}
}

func TestPlan_UnknownProfile(t *testing.T) {
	_, err := tiermigrate.Plan("bogus", profile.Small)
	if err == nil {
		t.Error("expected error for unknown from-profile, got nil")
	}

	_, err = tiermigrate.Plan(profile.Edge, "bogus")
	if err == nil {
		t.Error("expected error for unknown to-profile, got nil")
	}
}

func TestPlan_StepDetails_EdgeToSmall(t *testing.T) {
	plan, err := tiermigrate.Plan(profile.Edge, profile.Small)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, step := range plan.Steps {
		if step.Kind == tiermigrate.StepMigrateDB {
			if step.From != "sqlite" {
				t.Errorf("StepMigrateDB From = %q, want %q", step.From, "sqlite")
			}
			if step.To != "postgres" {
				t.Errorf("StepMigrateDB To = %q, want %q", step.To, "postgres")
			}
			return
		}
	}
	t.Error("StepMigrateDB not found in plan")
}
