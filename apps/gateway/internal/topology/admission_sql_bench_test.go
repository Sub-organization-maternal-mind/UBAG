package topology

import (
	"context"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/storekit/benchutil"
)

// BenchmarkPostgresAdmissionAcquireRelease measures the shared-admission token
// acquire + release on the lanes the create path uses (one dynamic target lane
// plus the tenant and global ceilings), 100 concurrent clients, one tenant.
func BenchmarkPostgresAdmissionAcquireRelease(b *testing.B) {
	db := benchutil.OpenPostgres(b)
	backend := NewPostgresTokenBackend(db)
	lanes := []Lane{
		{Key: "bench-lane|mock|app_bench", Cap: 1 << 20, Dynamic: true},
		{Key: "tenant:bench", Cap: 1 << 20},
		{Key: "global:all", Cap: 1 << 20},
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
