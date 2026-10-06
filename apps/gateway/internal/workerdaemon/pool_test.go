package workerdaemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The unset knobs are bounded too: a pool must never wait or queue without limit.
func TestPoolDefaultsAreBounded(t *testing.T) {
	p := &Pool{Size: 3}
	if got := p.maxWait(); got != 30*time.Second {
		t.Fatalf("default wait = %v, want 30s", got)
	}
	if got := p.maxQueue(); got != 3 {
		t.Fatalf("default queue bound = %d, want the pool size", got)
	}
	if got := p.retryAfter(); got != 2*time.Second {
		t.Fatalf("default retry-after = %v, want 2s", got)
	}
}

func TestPoolSizeIsBounded(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 1}, {-3, 1}, {1, 1}, {4, 4}, {32, 32}, {500, 32}} {
		if got := (&Pool{Size: tc.in}).Slots(); got != tc.want {
			t.Fatalf("Size=%d -> %d slots, want %d", tc.in, got, tc.want)
		}
	}
}

// The identity gate and the bounded queue do not need a daemon: a held slot that
// never touches its process is enough.
func TestPoolIdentityGateAndBoundedQueue(t *testing.T) {
	p := &Pool{Size: 2, MaxWait: 50 * time.Millisecond, MaxQueue: 1}
	t.Cleanup(p.Close)

	hold, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = p.Run(context.Background(), "k1", func(*Slot) error { close(hold); <-release; return nil })
	}()
	<-hold

	// Same identity, a free slot exists: it still has to wait for the holder.
	err := p.Run(context.Background(), "k1", func(*Slot) error { t.Error("ran beside its identity"); return nil })
	var over *OverloadError
	if !errors.As(err, &over) || over.Reason != "wait_expired" || !errors.Is(err, ErrOverloaded) {
		t.Fatalf("same identity = %v, want a wait_expired overload", err)
	}
	// Another identity takes the free slot at once.
	if err := p.Run(context.Background(), "k2", func(*Slot) error { return nil }); err != nil {
		t.Fatalf("other identity: %v", err)
	}
	close(release)
	wg.Wait()
}

func TestPoolClosedRefusesWork(t *testing.T) {
	p := &Pool{Size: 1}
	p.Close()
	if err := p.Run(context.Background(), "k", func(*Slot) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed pool = %v, want ErrClosed", err)
	}
}

func TestIdentityKeyIsStable(t *testing.T) {
	if IdentityKey("dir:a", "t") != IdentityKey("dir:a", "t") || IdentityKey("dir:a", "t") == IdentityKey("dir:a", "u") {
		t.Fatal("identity key must be deterministic and target-sensitive")
	}
}
