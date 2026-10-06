package executor

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

func TestCoarseQueueReasonCoversTheKnownFineReasons(t *testing.T) {
	for fine, want := range map[string]string{
		"local_slots_busy": "waiting_for_worker", "queue_full": "waiting_for_worker", "wait_expired": "waiting_for_worker",
		"identity_busy": "waiting_for_identity", "voice_session_active": "waiting_for_identity",
		"voice_admission_in_flight": "waiting_for_identity",
		"no_capacity":               "waiting_for_capacity",
		"no_eligible_node":          "waiting_for_node", "affinity_unavailable": "waiting_for_node",
		"fleet_unreadable": "temporarily_unavailable", "helper_lost": "temporarily_unavailable",
		"attempt_lease_held": "temporarily_unavailable", "something_new": "temporarily_unavailable",
	} {
		if got := CoarseQueueReason(fine); got != want {
			t.Errorf("CoarseQueueReason(%q) = %q, want %q", fine, got, want)
		}
	}
}

func TestHoldBoardNilIsANoOp(t *testing.T) {
	var b *HoldBoard
	b.Note("j", "t", "a", "no_capacity")
	b.Clear("j")
	if _, ok := b.Lookup("j"); ok || len(b.Counts("", "")) != 0 {
		t.Fatal("a nil board must hold nothing")
	}
}

func TestHoldBoardSinceRestartsOnlyWhenTheReasonChanges(t *testing.T) {
	b := NewHoldBoard()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	b.Note("j", "t", "a", "identity_busy")
	now = now.Add(30 * time.Second)
	b.Note("j", "t", "a", "identity_busy")
	h, ok := b.Lookup("j")
	if !ok || !h.Since.Equal(now.Add(-30*time.Second)) {
		t.Fatalf("same reason must keep since: %+v ok=%v", h, ok)
	}
	b.Note("j", "t", "a", "no_capacity")
	if h, _ = b.Lookup("j"); !h.Since.Equal(now) || h.Fine != "no_capacity" {
		t.Fatalf("a new reason must restart since: %+v", h)
	}
	b.Clear("j")
	if _, ok := b.Lookup("j"); ok {
		t.Fatal("cleared hold still visible")
	}
}

func TestHoldBoardEntriesExpireAndTheBoardIsBounded(t *testing.T) {
	b := NewHoldBoard()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	b.Note("old", "t", "a", "no_capacity")
	now = now.Add(holdTTL)
	if _, ok := b.Lookup("old"); ok || len(b.Counts("", "")) != 0 {
		t.Fatal("an entry not refreshed for the TTL must not be reported")
	}
	for i := 0; i < maxHolds+50; i++ {
		b.Note(strings.Repeat("j", 1)+time.Duration(i).String(), "t", "a", "no_capacity")
	}
	if n := len(b.m); n != maxHolds { // the expired entry was pruned, the rest capped
		t.Fatalf("board size = %d, want %d", n, maxHolds)
	}
}

func TestHoldBoardCountsAreTenantScopedAndReasonsAreTokens(t *testing.T) {
	b := NewHoldBoard()
	b.Note("1", "t1", "a1", "identity_busy")
	b.Note("2", "t1", "a2", "identity_busy")
	b.Note("3", "t2", "a1", "no_capacity")
	b.Note("4", "t2", "a1", "Not A Token: node 10.0.0.9")
	if got := b.Counts("t1", ""); got["identity_busy"] != 2 || len(got) != 1 {
		t.Fatalf("t1 = %v", got)
	}
	if got := b.Counts("t1", "a2"); got["identity_busy"] != 1 {
		t.Fatalf("t1/a2 = %v", got)
	}
	all := b.Counts("", "")
	if all["identity_busy"] != 2 || all["no_capacity"] != 1 || all["other_hold"] != 1 || len(all) != 3 {
		t.Fatalf("operator view = %v", all)
	}
}

// The consumer notes a refused placement on the board and clears it once the job
// is assigned (it ran): a held job reads as held, and the entry is gone after.
func TestConsumerNotesAndClearsHolds(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	board := NewHoldBoard()
	g := newHoldRig(t, func(HelperPickRequest) (HelperPlacement, error) {
		if busy.Load() {
			return HelperPlacement{}, &HelperRetryError{Reason: "no_capacity", RetryAfter: 50 * time.Millisecond}
		}
		return HelperPlacement{}, ErrNoHelper
	}, func(c *WorkerConsumer) { c.PoolSize, c.AsyncHolds, c.Holds = 1, 8, board })
	job := g.enqueue(t)
	g.run(t)

	waitFor(t, 5*time.Second, "the held job to appear on the board", func() bool {
		h, ok := board.Lookup(job.ID)
		return ok && h.Fine == "no_capacity" && h.TenantID == job.TenantID
	})
	busy.Store(false)
	waitFor(t, 5*time.Second, "the job to complete", func() bool { return g.completed(job.ID) })
	if _, ok := board.Lookup(job.ID); ok {
		t.Fatal("a job that ran must not stay on the board")
	}
	if g.f.job(job.ID).Status != jobstore.StatusCompleted {
		t.Fatal("job did not complete")
	}
}
