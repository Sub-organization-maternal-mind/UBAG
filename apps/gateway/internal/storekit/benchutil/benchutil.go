// Package benchutil is the shared harness for env-gated Postgres benchmarks of
// the job-create path (P1.8). It is imported from _test.go files only. A bench
// skips unless UBAG_TEST_POSTGRES_DSN is set and the repo migrations are
// already applied (see docs/perf-fleet/slices/P1.8.md).
package benchutil

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Clients is the number of concurrent callers every bench drives, matching the
// acceptance harness's 100-client burst. Override with UBAG_BENCH_CLIENTS.
func Clients() int { return envInt("UBAG_BENCH_CLIENTS", 100) }

// OpenPostgres opens the bench database with the gateway's default pool
// (20 open / 5 idle); UBAG_BENCH_POOL_OPEN / UBAG_BENCH_POOL_IDLE override.
func OpenPostgres(b *testing.B) *sql.DB {
	b.Helper()
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		b.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	if err := CheckDSN(dsn, os.Getenv("UBAG_BENCH_ALLOW_HOST")); err != nil {
		b.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		b.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		b.Fatal(err)
	}
	db.SetMaxOpenConns(envInt("UBAG_BENCH_POOL_OPEN", 20))
	db.SetMaxIdleConns(envInt("UBAG_BENCH_POOL_IDLE", 5))
	b.Cleanup(func() { _ = db.Close() })
	return db
}

// CheckDSN refuses a bench target that could be a shared or production
// database: the benches write append-only audit rows and admission tokens.
// Every host (including fallbacks) must be loopback, private, a unix socket,
// or listed in allow (comma-separated, UBAG_BENCH_ALLOW_HOST), and the
// database name must contain "bench" or "test". Mirrors checkTarget in
// tests/load/acceptance.mjs.
func CheckDSN(dsn, allow string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("benchutil: UBAG_TEST_POSTGRES_DSN is not a valid DSN")
	}
	name := strings.ToLower(cfg.Database)
	if !strings.Contains(name, "bench") && !strings.Contains(name, "test") {
		return fmt.Errorf("benchutil: database name %q must contain \"bench\" or \"test\"; benches write append-only audit rows, never aim them at a shared or production database", cfg.Database)
	}
	allowed := map[string]bool{}
	for _, h := range strings.Split(allow, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" && !strings.Contains(h, "*") {
			allowed[h] = true
		}
	}
	hosts := []string{cfg.Host}
	for _, f := range cfg.Fallbacks {
		hosts = append(hosts, f.Host)
	}
	for _, h := range hosts {
		h = strings.ToLower(h)
		if strings.HasPrefix(h, "/") || h == "localhost" || strings.HasSuffix(h, ".localhost") || allowed[h] {
			continue
		}
		if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
			continue
		}
		return fmt.Errorf("benchutil: host %q is not loopback/private and is not listed in UBAG_BENCH_ALLOW_HOST (wildcards are ignored); never point benches at a shared or production database", h)
	}
	return nil
}

// Run drives op from Clients() goroutines until b.N calls were made, then
// reports the pool's wait count and time per op.
func Run(b *testing.B, db *sql.DB, op func(i int64)) {
	b.Helper()
	before := db.Stats()
	var next atomic.Int64
	var wg sync.WaitGroup
	b.ResetTimer()
	for c := 0; c < Clients(); c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := next.Add(1)
				if i > int64(b.N) {
					return
				}
				op(i)
			}
		}()
	}
	wg.Wait()
	b.StopTimer()
	after := db.Stats()
	b.ReportMetric(float64(after.WaitCount-before.WaitCount)/float64(b.N), "poolwaits/op")
	b.ReportMetric((after.WaitDuration-before.WaitDuration).Seconds()*1000/float64(b.N), "poolwait-ms/op")
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}
