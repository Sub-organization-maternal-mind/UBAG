package voice

import (
	"testing"
	"time"
)

func TestMediaCountersFrameAgeBucketsAndClosedDirections(t *testing.T) {
	m := NewMediaCounters()
	m.ObserveFrameAge(DirectionMic, 3*time.Millisecond)  // <= 5ms
	m.ObserveFrameAge(DirectionMic, 90*time.Millisecond) // <= 160ms
	m.ObserveFrameAge(DirectionMic, 5*time.Second)       // +Inf only
	m.ObserveFrameAge("tenant-123", time.Millisecond)    // not a direction: ignored
	snaps := m.SnapshotFrameAge()
	if len(snaps) != 2 || snaps[0].Direction != DirectionMic || snaps[1].Direction != DirectionSpeaker {
		t.Fatalf("snapshot must always list mic then speaker: %+v", snaps)
	}
	mic := snaps[0]
	if mic.Count != 3 || mic.Buckets[0] != 1 || mic.Buckets[5] != 1 {
		t.Fatalf("mic buckets = %+v", mic)
	}
	if mic.Sum < 5.09 || mic.Sum > 5.1 {
		t.Fatalf("mic sum = %v", mic.Sum)
	}
	if snaps[1].Count != 0 {
		t.Fatalf("speaker must be zero-valued: %+v", snaps[1])
	}
}
