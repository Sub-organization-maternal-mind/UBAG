package topology

import "testing"

const (
	ctTenant = "tenant_a"
	ctTarget = "mock"
	ctID     = "app_a"
)

// TestConcurrencyReleaseForJobIdempotentDoesNotStealTokens proves that repeated
// releases of the same job never free another job's slot on the shared lane —
// the property that makes release safe from every terminal path (worker, cancel,
// reaper) at once.
func TestConcurrencyReleaseForJobIdempotentDoesNotStealTokens(t *testing.T) {
	r := NewConcurrencyRegistry()
	r.Report(ctTenant, ConcurrencyView{Target: ctTarget, IdentityRef: ctID, CurrentCap: 2})

	if !r.Acquire(ctTenant, ctTarget, ctID) {
		t.Fatal("acquire A should succeed")
	}
	r.MarkAcquired("job_A", ctTenant, ctTarget, ctID)
	if !r.Acquire(ctTenant, ctTarget, ctID) {
		t.Fatal("acquire B should succeed")
	}
	r.MarkAcquired("job_B", ctTenant, ctTarget, ctID)
	if r.Acquire(ctTenant, ctTarget, ctID) {
		t.Fatal("lane should be at capacity (2) after A and B")
	}

	// Release A three times (models worker + cancel + reaper all firing).
	r.ReleaseForJob("job_A")
	r.ReleaseForJob("job_A")
	r.ReleaseForJob("job_A")

	// Exactly ONE slot must be free — A's. B's token must survive.
	if !r.Acquire(ctTenant, ctTarget, ctID) {
		t.Fatal("A's slot should be free after release")
	}
	if r.Acquire(ctTenant, ctTarget, ctID) {
		t.Fatal("repeated release of A stole B's token: lane admitted past its ceiling")
	}
}

// TestConcurrencyReleaseForJobNoopWhenNeverAcquired proves that releasing a job
// that never acquired a token (e.g. a gRPC- or workflow-created job the shared
// worker also drains) does not decrement the lane — the over-release fix.
func TestConcurrencyReleaseForJobNoopWhenNeverAcquired(t *testing.T) {
	r := NewConcurrencyRegistry()
	r.Report(ctTenant, ConcurrencyView{Target: ctTarget, IdentityRef: ctID, CurrentCap: 2})

	r.Acquire(ctTenant, ctTarget, ctID)
	r.MarkAcquired("http_1", ctTenant, ctTarget, ctID)
	r.Acquire(ctTenant, ctTarget, ctID)
	r.MarkAcquired("http_2", ctTenant, ctTarget, ctID)

	// A never-Acquired job reaches a terminal state and releases: must be a no-op.
	r.ReleaseForJob("grpc_never_acquired")

	if r.Acquire(ctTenant, ctTarget, ctID) {
		t.Fatal("releasing a never-acquired job freed a real job's slot")
	}
}

// TestConcurrencyReleaseForJobNilAndEmptySafe covers the nil-registry and blank
// job-ID guards.
func TestConcurrencyReleaseForJobNilAndEmptySafe(t *testing.T) {
	var nilReg *ConcurrencyRegistry
	nilReg.MarkAcquired("j", ctTenant, ctTarget, ctID) // must not panic
	nilReg.ReleaseForJob("j")                          // must not panic

	r := NewConcurrencyRegistry()
	r.MarkAcquired("", ctTenant, ctTarget, ctID) // blank job ID ignored
	r.ReleaseForJob("")                          // blank job ID no-op
	r.ReleaseForJob("unknown-job")               // unknown job no-op
}
