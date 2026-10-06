package workerdaemon

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// notForwarded lists worker-read UBAG_* names that are deliberately NOT in
// AllowedEnv (set by the helper/worker launcher, job options, or outside the
// perf program). A new worker-read flag must be forwarded or listed here.
var notForwarded = map[string]string{
	"UBAG_ANTIGRAVITY_ENABLED":         "adapter gate, not forwarded today",
	"UBAG_ARTIFACTS_DIR":               "artifact dir comes from job options",
	"UBAG_HELPER_PLANE":                "set by the helper launcher for its own workers",
	"UBAG_PROVIDER_CONFIG_":            "prefix of the per-provider names allowlisted individually",
	"UBAG_REGION":                      "region routing, not forwarded today",
	"UBAG_VOICE_CDP_ALLOWED_HOSTS":     "voice worker, not forwarded today",
	"UBAG_WORKER_EVENT_CLOCK":          "test clock switch",
	"UBAG_WORKER_IDENTITY_LOCK_DIR":    "lock dir default is derived by the worker",
	"UBAG_WORKER_PROBE_MIN_INTERVAL_S": "probe pacing default, not forwarded today",
	"UBAG_WORKER_ROAMING":              "region roaming, not forwarded today",
}

func TestWorkerReadFlagsAreForwardedOrExplicitlyExempt(t *testing.T) {
	root := filepath.Join("..", "..", "..", "worker", "ubag_worker")
	literal := regexp.MustCompile(`["'](UBAG_[A-Z0-9_]+)["']`)
	seen := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".py" {
			return err
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range literal.FindAllSubmatch(raw, -1) {
			seen[string(m[1])] = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk worker sources: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("found no worker-read UBAG_* names; the scan path is wrong")
	}
	for name, path := range seen {
		_, forwarded := AllowedEnv[name]
		_, exempt := notForwarded[name]
		if !forwarded && !exempt {
			t.Errorf("%s (read in %s) is neither in AllowedEnv nor in notForwarded", name, path)
		}
		if forwarded && exempt {
			t.Errorf("%s is forwarded; drop it from notForwarded", name)
		}
	}
}
