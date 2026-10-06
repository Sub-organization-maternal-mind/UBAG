package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/storekit/benchutil"
)

// BenchmarkPostgresCreate measures the job insert (+ queued event) under the
// acceptance harness's 100 concurrent clients. Env-gated; see benchutil.
func BenchmarkPostgresCreate(b *testing.B) {
	db := benchutil.OpenPostgres(b)
	store := NewPostgresStore(db)
	tenant := "tenant_bench_jobs_" + time.Now().UTC().Format("20060102150405")
	b.Cleanup(func() { _, _ = db.Exec(`DELETE FROM gateway_jobs WHERE tenant_id = $1`, tenant) })
	benchutil.Run(b, db, func(int64) {
		if _, err := store.Create(context.Background(), CreateRequest{
			APIVersion: "2026-05-22", TenantID: tenant, AppID: "app_bench", Target: "mock", CommandType: "chat.prompt",
			Input: map[string]any{"prompt": "bench"}, TraceID: "trace_bench",
		}); err != nil {
			b.Error(err)
		}
	})
}
