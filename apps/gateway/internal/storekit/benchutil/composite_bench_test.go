package benchutil_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/storekit/benchutil"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// BenchmarkPostgresCreatePathComposite runs the accept path's store calls in
// order (audit append, idempotency reserve, admission acquire on ONE lane, job
// create, admission associate, idempotency complete, admission release) from
// 100 clients on one tenant. UBAG_BENCH_SKIP is a comma list of components to
// leave out (audit, idempotency, admission, create), so each lock's share is an
// A/B between two runs rather than a guess (P1.8 review).
func BenchmarkPostgresCreatePathComposite(b *testing.B) {
	db := benchutil.OpenPostgres(b)
	skip := map[string]bool{}
	for _, c := range strings.Split(os.Getenv("UBAG_BENCH_SKIP"), ",") {
		skip[strings.TrimSpace(c)] = true
	}
	stamp := time.Now().UTC().Format("20060102150405")
	tenant := "tenant_bench_composite_" + stamp
	b.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM gateway_jobs WHERE tenant_id = $1`, tenant)
		_, _ = db.Exec(`DELETE FROM gateway_idempotency_records WHERE tenant_id = $1`, tenant)
	})
	auditStore := audit.NewPostgresStore(db)
	idemStore := idempotency.NewPostgresStore(db, time.Hour)
	jobStore := jobs.NewPostgresStore(db)
	adm := topology.NewPostgresTokenBackend(db)
	lanes := []topology.Lane{{Key: "bench-composite|" + stamp, Cap: 1 << 20, Dynamic: true}}

	benchutil.Run(b, db, func(i int64) {
		ctx := context.Background()
		if !skip["audit"] {
			if _, err := auditStore.Append(ctx, audit.Record{
				TenantID: tenant, AppID: "app_bench", Actor: "bench", Action: "authorize:job:create", Resource: "/v1/jobs",
				Outcome: "allow", OccurredAt: time.Now(), Attributes: map[string]any{"role": "app", "method": "POST"},
			}); err != nil {
				b.Errorf("audit: %v", err)
				return
			}
		}
		scope := idempotency.Scope{TenantID: tenant, AppID: "app_bench", Operation: "create_job", Key: "k" + strconv.FormatInt(i, 10)}
		if !skip["idempotency"] {
			if d, err := idemStore.Reserve(ctx, scope, "hash"); err != nil || d.Kind != idempotency.DecisionReserved {
				b.Errorf("reserve: %v %v", d.Kind, err)
				return
			}
		}
		token := ""
		if !skip["admission"] {
			id, ok, err := adm.AcquireToken(ctx, lanes, time.Minute, time.Now().UTC())
			if err != nil || !ok {
				b.Errorf("acquire ok=%v err=%v", ok, err)
				return
			}
			token = id
		}
		jobID := "job_x"
		if !skip["create"] {
			job, err := jobStore.Create(ctx, jobs.CreateRequest{
				APIVersion: "2026-05-22", TenantID: tenant, AppID: "app_bench", Target: "mock", CommandType: "chat.prompt",
				Input: map[string]any{"prompt": "bench"}, TraceID: "trace_bench",
			})
			if err != nil {
				b.Errorf("create: %v", err)
				return
			}
			jobID = job.ID
		}
		if token != "" {
			if err := adm.AssociateToken(ctx, token, jobID); err != nil {
				b.Errorf("associate: %v", err)
			}
		}
		if !skip["idempotency"] {
			if err := idemStore.Complete(ctx, scope, "hash", jobID, 202); err != nil {
				b.Errorf("complete: %v", err)
			}
		}
		if token != "" {
			if err := adm.ReleaseToken(ctx, token); err != nil {
				b.Errorf("release: %v", err)
			}
		}
	})
}
