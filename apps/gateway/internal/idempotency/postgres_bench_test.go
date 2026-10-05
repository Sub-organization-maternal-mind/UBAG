package idempotency

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/storekit/benchutil"
)

// BenchmarkPostgresReserveComplete measures the create path's two idempotency
// calls (fresh key reserve, then complete) under 100 concurrent clients.
func BenchmarkPostgresReserveComplete(b *testing.B) {
	db := benchutil.OpenPostgres(b)
	store := NewPostgresStore(db, time.Hour)
	tenant := "tenant_bench_idem_" + time.Now().UTC().Format("20060102150405")
	b.Cleanup(func() { _, _ = db.Exec(`DELETE FROM gateway_idempotency_records WHERE tenant_id = $1`, tenant) })
	benchutil.Run(b, db, func(i int64) {
		scope := Scope{TenantID: tenant, AppID: "app_bench", Operation: "create_job", Key: "k" + strconv.FormatInt(i, 10)}
		if d, err := store.Reserve(context.Background(), scope, "hash"); err != nil || d.Kind != DecisionReserved {
			b.Errorf("Reserve = %v, %v", d.Kind, err)
			return
		}
		if err := store.Complete(context.Background(), scope, "hash", "job_x", 202); err != nil {
			b.Error(err)
		}
	})
}
