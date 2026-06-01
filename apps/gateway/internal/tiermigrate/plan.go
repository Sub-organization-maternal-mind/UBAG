// Package tiermigrate computes an ordered MigrationPlan of steps needed to
// move a deployment from one UBAG profile tier to a higher one. Downgrades
// are not supported.
package tiermigrate

import (
	"errors"
	"fmt"

	"github.com/ubag/ubag/apps/gateway/internal/profile"
)

// ErrDowngradeUnsupported is returned when the from profile is higher than to.
var ErrDowngradeUnsupported = errors.New("tier migration: downgrades are not supported")

// StepKind identifies the type of migration step.
type StepKind string

const (
	StepMigrateDB       StepKind = "migrate-db"         // SQLite→Postgres (or Postgres→Postgres-HA)
	StepEnableCache     StepKind = "enable-cache"        // Enable semantic cache
	StepEnableRBAC      StepKind = "enable-rbac"         // Enable multi-tenant RBAC
	StepEnableSSO       StepKind = "enable-sso"          // Enable SSO
	StepEnableSCIM      StepKind = "enable-scim"         // Enable SCIM provisioning
	StepSwitchAudit     StepKind = "switch-audit"        // Switch audit delivery
	StepEnableGeoRepl   StepKind = "enable-geo-repl"    // Enable geo-replication
	StepEnableCompliance StepKind = "enable-compliance"  // Enable compliance modes
	StepSwitchExecutor  StepKind = "switch-executor"     // Switch job executor (NATS/file)
	StepSwitchArtifacts StepKind = "switch-artifacts"    // Switch to MinIO
	StepUpgradeBrowser  StepKind = "upgrade-browser"     // Increase browser session pool
)

// MigrationStep is a single actionable step in a migration plan.
type MigrationStep struct {
	Kind        StepKind
	Description string
	From        string
	To          string
}

// MigrationPlan is the ordered set of steps to migrate between two profiles.
type MigrationPlan struct {
	FromProfile profile.Profile
	ToProfile   profile.Profile
	Steps       []MigrationStep
}

// orderedProfiles lists all profiles in increasing capability order.
var orderedProfiles = []profile.Profile{profile.Edge, profile.Small, profile.Standard, profile.Enterprise}

// Plan computes the migration steps from `from` to `to` profile.
// Upgrades only; returns ErrDowngradeUnsupported for same or lower tier.
func Plan(from, to profile.Profile) (*MigrationPlan, error) {
	fromIdx := profileIndex(from)
	toIdx := profileIndex(to)
	if fromIdx < 0 {
		return nil, fmt.Errorf("unknown profile: %q", from)
	}
	if toIdx < 0 {
		return nil, fmt.Errorf("unknown profile: %q", to)
	}
	if toIdx <= fromIdx {
		return nil, ErrDowngradeUnsupported
	}

	plan := &MigrationPlan{
		FromProfile: from,
		ToProfile:   to,
	}

	// Walk through each intermediate upgrade (e.g. edge→small→standard→enterprise)
	for i := fromIdx; i < toIdx; i++ {
		steps := diffProfiles(orderedProfiles[i], orderedProfiles[i+1])
		plan.Steps = append(plan.Steps, steps...)
	}
	return plan, nil
}

func profileIndex(p profile.Profile) int {
	for i, op := range orderedProfiles {
		if op == p {
			return i
		}
	}
	return -1
}

func diffProfiles(from, to profile.Profile) []MigrationStep {
	fromF := from.Features()
	toF := to.Features()
	var steps []MigrationStep

	// DB backend change
	if fromF.JobBackend != toF.JobBackend {
		steps = append(steps, MigrationStep{
			Kind:        StepMigrateDB,
			Description: fmt.Sprintf("Migrate job backend from %s to %s", fromF.JobBackend, toF.JobBackend),
			From:        string(fromF.JobBackend),
			To:          string(toF.JobBackend),
		})
	}

	// Semantic cache
	if fromF.SemanticCache < toF.SemanticCache {
		steps = append(steps, MigrationStep{
			Kind:        StepEnableCache,
			Description: "Enable semantic cache",
			From:        fromF.SemanticCache.String(),
			To:          toF.SemanticCache.String(),
		})
	}

	// RBAC
	if fromF.MultiTenantRBAC < toF.MultiTenantRBAC {
		steps = append(steps, MigrationStep{
			Kind:        StepEnableRBAC,
			Description: "Enable multi-tenant RBAC",
			From:        fromF.MultiTenantRBAC.String(),
			To:          toF.MultiTenantRBAC.String(),
		})
	}

	// SSO
	if fromF.SSO < toF.SSO {
		steps = append(steps, MigrationStep{
			Kind:        StepEnableSSO,
			Description: "Enable SSO",
			From:        fromF.SSO.String(),
			To:          toF.SSO.String(),
		})
	}

	// SCIM
	if fromF.SCIM < toF.SCIM {
		steps = append(steps, MigrationStep{
			Kind:        StepEnableSCIM,
			Description: "Enable SCIM provisioning",
			From:        fromF.SCIM.String(),
			To:          toF.SCIM.String(),
		})
	}

	// Audit delivery
	if fromF.AuditDelivery != toF.AuditDelivery {
		steps = append(steps, MigrationStep{
			Kind:        StepSwitchAudit,
			Description: fmt.Sprintf("Switch audit delivery from %s to %s", fromF.AuditDelivery, toF.AuditDelivery),
			From:        string(fromF.AuditDelivery),
			To:          string(toF.AuditDelivery),
		})
	}

	// Geo-replication
	if fromF.GeoReplication < toF.GeoReplication {
		steps = append(steps, MigrationStep{
			Kind:        StepEnableGeoRepl,
			Description: "Enable geo-replication",
			From:        fromF.GeoReplication.String(),
			To:          toF.GeoReplication.String(),
		})
	}

	// Browser session pool (only emit step when limit actually changes)
	if fromF.BrowserSessionPoolMax != toF.BrowserSessionPoolMax {
		fromPool := fmt.Sprintf("%d", fromF.BrowserSessionPoolMax)
		toPool := fmt.Sprintf("%d", toF.BrowserSessionPoolMax)
		if toF.BrowserSessionPoolMax == profile.UnlimitedSessions {
			toPool = "unlimited"
		}
		if fromF.BrowserSessionPoolMax == profile.UnlimitedSessions {
			fromPool = "unlimited"
		}
		steps = append(steps, MigrationStep{
			Kind:        StepUpgradeBrowser,
			Description: fmt.Sprintf("Upgrade browser session pool from %s to %s", fromPool, toPool),
			From:        fromPool,
			To:          toPool,
		})
	}

	return steps
}
