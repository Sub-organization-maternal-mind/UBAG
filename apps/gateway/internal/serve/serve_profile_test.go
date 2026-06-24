package serve

import (
	"os"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/profile"
)

func TestProfileGating(t *testing.T) {
	tests := []struct {
		name           string
		profileName    string
		geoEnvOverride string
		wantGeo        bool
		wantMFA        bool
	}{
		{"edge-no-geo", "edge", "", false, false},
		{"small-no-geo", "small", "", false, false},
		{"standard-no-geo", "standard", "", false, true}, // SSO=On → MFA enabled
		{"enterprise-geo", "enterprise", "", true, true},
		{"edge-override", "edge", "1", true, false}, // geo override, but SSO=Off → no MFA
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UBAG_PROFILE", tc.profileName)
			if tc.geoEnvOverride != "" {
				t.Setenv("UBAG_ENABLE_GEO_REPLICATION", tc.geoEnvOverride)
			}

			prof, err := profile.ParseOrDefault(tc.profileName)
			if err != nil {
				t.Fatalf("ParseOrDefault(%q): %v", tc.profileName, err)
			}
			feat := prof.Features()

			geoEnabled := feat.GeoReplication == profile.On ||
				strings.EqualFold(strings.TrimSpace(os.Getenv("UBAG_ENABLE_GEO_REPLICATION")), "1")
			mfaEnabled := feat.SSO.Enabled() ||
				strings.EqualFold(strings.TrimSpace(os.Getenv("UBAG_ENABLE_MFA")), "1")

			if geoEnabled != tc.wantGeo {
				t.Errorf("geoEnabled=%v want=%v", geoEnabled, tc.wantGeo)
			}
			if mfaEnabled != tc.wantMFA {
				t.Errorf("mfaEnabled=%v want=%v", mfaEnabled, tc.wantMFA)
			}
		})
	}
}
