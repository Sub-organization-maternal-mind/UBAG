// Package profile implements the UBAG deployment-profile system described in
// blueprint §4. A single codebase runs across four profiles — edge, small,
// standard, and enterprise — selected at startup by the UBAG_PROFILE environment
// variable. Each profile enables a different slice of the §4.5 feature matrix.
//
// The matrix is encoded as data here so that the rest of the gateway can ask
// capability questions (profile.Features().SSO == profile.On) instead of
// hard-coding tier logic. Profiles never relax tenant-isolation or security
// guarantees; they only gate optional surfaces and set capacity ceilings.
package profile

import (
	"fmt"
	"strings"
)

// Profile is a deployment tier. The zero value is invalid; use Parse or
// ParseOrDefault to obtain one.
type Profile string

const (
	// Edge is the single-binary, zero-dependency tier (SQLite, embedded queue,
	// local filesystem object store). Honors principle #8: lightweight mode
	// must always work.
	Edge Profile = "edge"
	// Small is a single-VM docker-compose deployment (Postgres + Dragonfly +
	// MinIO + observability).
	Small Profile = "small"
	// Standard is a Kubernetes deployment with HA Postgres, clustered queue and
	// cache, and the full observability stack.
	Standard Profile = "standard"
	// Enterprise is multi-region Kubernetes with geo-replication and the full
	// compliance/SSO/SCIM surface.
	Enterprise Profile = "enterprise"
)

// Default is the profile used when UBAG_PROFILE is unset. Edge keeps the
// "clone and make dev in five minutes" contract (principle #8).
const Default = Edge

// all lists the profiles in increasing capability order.
var all = []Profile{Edge, Small, Standard, Enterprise}

// FeatureState is a tri-state capability flag matching the §4.5 matrix cells,
// which are one of: "—" (Off), "optional", or "✓" (On).
type FeatureState int

const (
	// Off means the feature is unavailable on this profile.
	Off FeatureState = iota
	// Optional means the feature can be enabled by explicit configuration but is
	// not on by default.
	Optional
	// On means the feature is available (and, where applicable, enabled) by
	// default on this profile.
	On
)

func (s FeatureState) String() string {
	switch s {
	case Off:
		return "off"
	case Optional:
		return "optional"
	case On:
		return "on"
	default:
		return "unknown"
	}
}

// Enabled reports whether the feature is available at all (On or Optional).
func (s FeatureState) Enabled() bool { return s == On || s == Optional }

// UnlimitedSessions is the sentinel for an unbounded browser-session pool.
const UnlimitedSessions = -1

// JobBackend identifies the persistence backend a profile expects.
type JobBackend string

const (
	BackendSQLite      JobBackend = "sqlite"
	BackendPostgres    JobBackend = "postgres"
	BackendPostgresHA  JobBackend = "postgres-ha"
	BackendMultiRegion JobBackend = "multi-region"
)

// AuditDelivery captures the §4.5 "Audit log → SIEM" cell.
type AuditDelivery string

const (
	AuditNone          AuditDelivery = "none"
	AuditLocalFile     AuditDelivery = "local-file"
	AuditSIEM          AuditDelivery = "siem"
	AuditSIEMImmutable AuditDelivery = "siem-immutable"
)

// TracingDelivery captures the §4.5 "Distributed tracing" cell.
type TracingDelivery string

const (
	TracingLocal TracingDelivery = "local"
	TracingFull  TracingDelivery = "full"
)

// Features is the resolved §4.5 capability set for a profile. Fields that are
// always-on across every tier (REST/WS, gRPC, SDKs, webhooks, idempotency) are
// represented as plain bools; tri-state cells use FeatureState.
type Features struct {
	RESTAndWebSocket      bool
	GRPC                  bool
	Webhooks              bool
	IdempotencyRetriesDLQ bool
	AdminDashboard        bool

	PersistentJobs        bool
	JobBackend            JobBackend
	BrowserSessionPoolMax int // UnlimitedSessions (-1) means no fixed ceiling.

	SemanticCache   FeatureState
	MultiTenantRBAC FeatureState
	SSO             FeatureState
	SCIM            FeatureState
	DashboardSSO    bool
	AuditDelivery   AuditDelivery
	Tracing         TracingDelivery
	GeoReplication  FeatureState
	ComplianceModes FeatureState // HIPAA / GDPR modes
}

// matrix encodes blueprint §4.5 verbatim. Keep this table aligned with the
// document; it is the single source of truth for tier capabilities.
var matrix = map[Profile]Features{
	Edge: {
		RESTAndWebSocket:      true,
		GRPC:                  true,
		Webhooks:              true,
		IdempotencyRetriesDLQ: true,
		AdminDashboard:        true,
		PersistentJobs:        true,
		JobBackend:            BackendSQLite,
		BrowserSessionPoolMax: 3,
		SemanticCache:         Optional,
		MultiTenantRBAC:       Off,
		SSO:                   Off,
		SCIM:                  Off,
		DashboardSSO:          false,
		AuditDelivery:         AuditNone,
		Tracing:               TracingLocal,
		GeoReplication:        Off,
		ComplianceModes:       Optional,
	},
	Small: {
		RESTAndWebSocket:      true,
		GRPC:                  true,
		Webhooks:              true,
		IdempotencyRetriesDLQ: true,
		AdminDashboard:        true,
		PersistentJobs:        true,
		JobBackend:            BackendPostgres,
		BrowserSessionPoolMax: 30,
		SemanticCache:         On,
		MultiTenantRBAC:       On,
		SSO:                   Off,
		SCIM:                  Off,
		DashboardSSO:          false,
		AuditDelivery:         AuditLocalFile,
		Tracing:               TracingFull,
		GeoReplication:        Off,
		ComplianceModes:       On,
	},
	Standard: {
		RESTAndWebSocket:      true,
		GRPC:                  true,
		Webhooks:              true,
		IdempotencyRetriesDLQ: true,
		AdminDashboard:        true,
		PersistentJobs:        true,
		JobBackend:            BackendPostgresHA,
		BrowserSessionPoolMax: UnlimitedSessions,
		SemanticCache:         On,
		MultiTenantRBAC:       On,
		SSO:                   On,
		SCIM:                  Optional,
		DashboardSSO:          false,
		AuditDelivery:         AuditSIEM,
		Tracing:               TracingFull,
		GeoReplication:        Optional,
		ComplianceModes:       On,
	},
	Enterprise: {
		RESTAndWebSocket:      true,
		GRPC:                  true,
		Webhooks:              true,
		IdempotencyRetriesDLQ: true,
		AdminDashboard:        true,
		PersistentJobs:        true,
		JobBackend:            BackendMultiRegion,
		BrowserSessionPoolMax: UnlimitedSessions,
		SemanticCache:         On,
		MultiTenantRBAC:       On,
		SSO:                   On,
		SCIM:                  On,
		DashboardSSO:          true,
		AuditDelivery:         AuditSIEMImmutable,
		Tracing:               TracingFull,
		GeoReplication:        On,
		ComplianceModes:       On,
	},
}

// Parse resolves a profile name (case-insensitive, surrounding whitespace
// trimmed). An empty string is rejected; callers that want the default should
// use ParseOrDefault.
func Parse(raw string) (Profile, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", fmt.Errorf("profile: empty UBAG_PROFILE (expected one of %s)", joinProfiles())
	}
	p := Profile(name)
	if _, ok := matrix[p]; !ok {
		return "", fmt.Errorf("profile: unsupported UBAG_PROFILE %q (expected one of %s)", raw, joinProfiles())
	}
	return p, nil
}

// ParseOrDefault behaves like Parse but returns Default for an empty input.
// A non-empty but unknown value is still an error.
func ParseOrDefault(raw string) (Profile, error) {
	if strings.TrimSpace(raw) == "" {
		return Default, nil
	}
	return Parse(raw)
}

// Valid reports whether p is a known profile.
func (p Profile) Valid() bool {
	_, ok := matrix[p]
	return ok
}

func (p Profile) String() string { return string(p) }

// Features returns the resolved §4.5 capability set. It panics only on an
// invalid Profile, which Parse/ParseOrDefault make unreachable in practice;
// callers constructing a Profile directly should check Valid first.
func (p Profile) Features() Features {
	f, ok := matrix[p]
	if !ok {
		panic(fmt.Sprintf("profile: Features called on invalid profile %q", p))
	}
	return f
}

// AtLeast reports whether p is at or above other in capability order
// (edge < small < standard < enterprise). Useful for "standard and up" gates.
func (p Profile) AtLeast(other Profile) bool {
	return rank(p) >= rank(other)
}

// All returns the profiles in increasing-capability order.
func All() []Profile {
	out := make([]Profile, len(all))
	copy(out, all)
	return out
}

func rank(p Profile) int {
	for i, candidate := range all {
		if candidate == p {
			return i
		}
	}
	return -1
}

func joinProfiles() string {
	names := make([]string, len(all))
	for i, p := range all {
		names[i] = string(p)
	}
	return strings.Join(names, ", ")
}
