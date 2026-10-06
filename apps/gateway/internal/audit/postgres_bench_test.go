package audit

import (
	"context"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/storekit/benchutil"
)

// BenchmarkPostgresAppendOneTenant measures the per-request authorization
// audit append when every client belongs to one tenant (the acceptance
// harness shape): appends serialize on the tenant's chain.
func BenchmarkPostgresAppendOneTenant(b *testing.B) {
	db := benchutil.OpenPostgres(b)
	store := NewPostgresStore(db)
	tenant := "tenant_bench_audit_" + time.Now().UTC().Format("20060102150405")
	benchutil.Run(b, db, func(int64) {
		if _, err := store.Append(context.Background(), Record{
			TenantID: tenant, AppID: "app_bench", Actor: "bench", Action: "authorize:job:create", Resource: "/v1/jobs",
			Outcome: "allow", OccurredAt: time.Now(), Attributes: map[string]any{"role": "app", "method": "POST"},
		}); err != nil {
			b.Error(err)
		}
	})
}
