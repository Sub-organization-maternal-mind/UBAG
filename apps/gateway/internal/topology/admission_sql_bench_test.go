package topology

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/storekit/benchutil"
)

// BenchmarkPostgresAdmissionAcquireRelease measures the shared-admission token
// acquire + release, 100 concurrent clients, one tenant. By default it uses ONE
// dynamic target lane, the production shape: the tenant and global lanes exist
// only when UBAG_ADMISSION_MAX_INFLIGHT_* is set (concurrency.go
// acquireShared), and docker-compose.vps.yml leaves them empty. Set
// UBAG_BENCH_ADMISSION_EXTRA_LANES=1 to add bench-only tenant and global lanes
// (never the real tenant:<id> or global:all keys) and measure that
// configuration (about twice the cost, one lock per lane).
func BenchmarkPostgresAdmissionAcquireRelease(b *testing.B) {
	db := benchutil.OpenPostgres(b)
	backend := NewPostgresTokenBackend(db)
	lanes := []Lane{{Key: "bench-lane|mock|app_bench", Cap: 1 << 20, Dynamic: true}}
	if os.Getenv("UBAG_BENCH_ADMISSION_EXTRA_LANES") == "1" {
		lanes = append(lanes, Lane{Key: "bench:tenant", Cap: 1 << 20}, Lane{Key: "bench:global", Cap: 1 << 20})
	}
	benchutil.Run(b, db, func(int64) {
		ctx := context.Background()
		id, ok, err := backend.AcquireToken(ctx, lanes, time.Minute, time.Now().UTC())
		if err != nil || !ok {
			b.Errorf("AcquireToken ok=%v err=%v", ok, err)
			return
		}
		if err := backend.ReleaseToken(ctx, id); err != nil {
			b.Error(err)
		}
	})
}
