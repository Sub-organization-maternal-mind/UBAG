package workerdaemon

import "testing"

// A worker-read isolation flag that FilterEnv drops leaves the worker silently in
// legacy mode, so these must stay forwardable.
func TestAllowedEnvForwardsIsolationFlags(t *testing.T) {
	for _, key := range []string{"UBAG_PROFILE_OPTIONS_POLICY", "UBAG_WARM_RESUME_FASTPATH"} {
		if _, ok := AllowedEnv[key]; !ok {
			t.Errorf("%s must be in AllowedEnv", key)
		}
	}
}
