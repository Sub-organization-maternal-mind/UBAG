package profile

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Profile
		wantErr bool
	}{
		{name: "edge", in: "edge", want: Edge},
		{name: "small", in: "small", want: Small},
		{name: "standard", in: "standard", want: Standard},
		{name: "enterprise", in: "enterprise", want: Enterprise},
		{name: "uppercase", in: "ENTERPRISE", want: Enterprise},
		{name: "whitespace", in: "  small  ", want: Small},
		{name: "empty", in: "", wantErr: true},
		{name: "unknown", in: "mega", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("Parse(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseOrDefault(t *testing.T) {
	got, err := ParseOrDefault("")
	if err != nil {
		t.Fatalf("ParseOrDefault(\"\") unexpected error: %v", err)
	}
	if got != Default {
		t.Fatalf("ParseOrDefault(\"\") = %q, want %q", got, Default)
	}
	if Default != Edge {
		t.Fatalf("Default = %q, want edge (principle #8 lightweight default)", Default)
	}
	if _, err := ParseOrDefault("bogus"); err == nil {
		t.Fatal("ParseOrDefault(\"bogus\") expected error")
	}
}

func TestEveryProfileHasFeatures(t *testing.T) {
	for _, p := range All() {
		if !p.Valid() {
			t.Errorf("profile %q reports invalid", p)
		}
		// Must not panic and must set always-on capabilities.
		f := p.Features()
		if !f.RESTAndWebSocket || !f.GRPC || !f.Webhooks || !f.IdempotencyRetriesDLQ || !f.AdminDashboard {
			t.Errorf("profile %q missing an always-on capability: %+v", p, f)
		}
		if !f.PersistentJobs || f.JobBackend == "" {
			t.Errorf("profile %q must have persistent jobs and a backend", p)
		}
	}
}

func TestMatrixFidelity(t *testing.T) {
	// Spot-checks against specific §4.5 cells to catch table drift.
	cases := []struct {
		profile Profile
		check   func(Features) bool
		desc    string
	}{
		{Edge, func(f Features) bool { return f.JobBackend == BackendSQLite }, "edge persists to sqlite"},
		{Edge, func(f Features) bool { return f.BrowserSessionPoolMax == 3 }, "edge session pool <= 3"},
		{Edge, func(f Features) bool { return f.SemanticCache == Optional }, "edge semantic cache optional"},
		{Edge, func(f Features) bool { return f.MultiTenantRBAC == Off }, "edge has no multi-tenant RBAC"},
		{Edge, func(f Features) bool { return f.SSO == Off }, "edge has no SSO"},
		{Edge, func(f Features) bool { return f.Tracing == TracingLocal }, "edge tracing is local"},
		{Small, func(f Features) bool { return f.JobBackend == BackendPostgres }, "small persists to postgres"},
		{Small, func(f Features) bool { return f.BrowserSessionPoolMax == 30 }, "small session pool <= 30"},
		{Small, func(f Features) bool { return f.SemanticCache == On }, "small semantic cache on"},
		{Small, func(f Features) bool { return f.MultiTenantRBAC == On }, "small multi-tenant RBAC on"},
		{Small, func(f Features) bool { return f.AuditDelivery == AuditLocalFile }, "small audit to local file"},
		{Standard, func(f Features) bool { return f.SSO == On }, "standard SSO on"},
		{Standard, func(f Features) bool { return f.SCIM == Optional }, "standard SCIM optional"},
		{Standard, func(f Features) bool { return f.BrowserSessionPoolMax == UnlimitedSessions }, "standard unlimited sessions"},
		{Standard, func(f Features) bool { return f.GeoReplication == Optional }, "standard geo-replication optional"},
		{Enterprise, func(f Features) bool { return f.SCIM == On }, "enterprise SCIM on"},
		{Enterprise, func(f Features) bool { return f.DashboardSSO }, "enterprise dashboard + SSO"},
		{Enterprise, func(f Features) bool { return f.AuditDelivery == AuditSIEMImmutable }, "enterprise immutable audit"},
		{Enterprise, func(f Features) bool { return f.GeoReplication == On }, "enterprise geo-replication on"},
		{Enterprise, func(f Features) bool { return f.JobBackend == BackendMultiRegion }, "enterprise multi-region backend"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if !c.check(c.profile.Features()) {
				t.Errorf("§4.5 fidelity failed: %s (profile %q -> %+v)", c.desc, c.profile, c.profile.Features())
			}
		})
	}
}

func TestAtLeast(t *testing.T) {
	if !Standard.AtLeast(Small) {
		t.Error("standard should be at least small")
	}
	if Small.AtLeast(Standard) {
		t.Error("small should not be at least standard")
	}
	if !Enterprise.AtLeast(Enterprise) {
		t.Error("enterprise should be at least itself")
	}
	if !Edge.AtLeast(Edge) {
		t.Error("edge should be at least itself")
	}
}

func TestFeatureState(t *testing.T) {
	if !On.Enabled() || !Optional.Enabled() {
		t.Error("On and Optional must report Enabled")
	}
	if Off.Enabled() {
		t.Error("Off must not report Enabled")
	}
	if On.String() != "on" || Optional.String() != "optional" || Off.String() != "off" {
		t.Errorf("unexpected FeatureState strings: %s/%s/%s", On, Optional, Off)
	}
}
