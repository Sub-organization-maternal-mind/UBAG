package runner

import (
	"os"
	"testing"
)

// Helper-hosted voice needs the worker to attach to the node's OWN loopback
// browser: the helper adds UBAG_VOICE_CDP_ALLOWED_HOSTS=127.0.0.1 itself. A job
// never chooses it, and the extra environment can never smuggle in what the
// worker environment exists to keep out.
func TestExtraEnvReachesEveryWorkerButNeverCarriesPrimaryConfiguration(t *testing.T) {
	r, err := New(Config{
		Python: os.Args[0], Script: "run_worker_daemon.py", Slots: 2,
		ExtraEnv: []string{"UBAG_VOICE_CDP_ALLOWED_HOSTS=127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	for slot := range 2 {
		env := r.pool.SlotCommand(slot).Env
		if v, ok := lookup(env, "UBAG_VOICE_CDP_ALLOWED_HOSTS"); !ok || v != "127.0.0.1" {
			t.Fatalf("slot %d UBAG_VOICE_CDP_ALLOWED_HOSTS = %q, %v", slot, v, ok)
		}
		if v, _ := lookup(env, "UBAG_HELPER_PLANE"); v != "1" {
			t.Fatalf("slot %d lost helper mode: %q", slot, v)
		}
	}

	// Without the voice setting the worker has no CDP allowlist: voice jobs are
	// rejected by the worker itself (fail closed).
	plain, err := New(Config{Python: os.Args[0], Script: "run_worker_daemon.py", Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plain.Close)
	if _, ok := lookup(plain.pool.SlotCommand(0).Env, "UBAG_VOICE_CDP_ALLOWED_HOSTS"); ok {
		t.Fatal("the CDP allowlist must exist only when the helper set it")
	}

	for _, bad := range []string{
		"UBAG_REMOTE_BROWSER_ENDPOINT=http://x", "UBAG_NOVNC_BASE_URL=http://x", "UBAG_APP_SECRET=x",
		"UBAG_VOICE_TURN_SECRET=x", "UBAG_DATABASE_URL=x", "UBAG_HELPER_PLANE=0", "UBAG_PROFILE_DIR=/elsewhere",
	} {
		if _, err := New(Config{Python: os.Args[0], Script: "x", ExtraEnv: []string{bad}}); err == nil {
			t.Errorf("%s must be refused as extra worker environment", bad)
		}
	}
}
