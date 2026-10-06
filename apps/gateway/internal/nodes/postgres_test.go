package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// openNodesPostgres returns a migrated database, or skips the test. Like every
// *_postgres_test.go here it needs UBAG_TEST_POSTGRES_DSN (run via
// `node tools/run-postgres-roundtrip-tests.mjs --apply-migrations`, which CI
// dispatches); without it the test skips.
func openNodesPostgres(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("PingContext: %v", err)
	}
	// 0001 creates gateway_schema_migrations; both files are idempotent.
	for _, name := range []string{"0001_gateway_stores.sql", "0023_helper_nodes.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", "postgres", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.ExecContext(context.Background(), string(body)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	return db
}

func cleanupNodesPostgres(t *testing.T, db *sql.DB, prefix string) {
	t.Helper()
	// State first: it references allocations.
	for _, table := range []string{"gateway_helper_state", "gateway_helper_allocations", "gateway_helper_registry"} {
		if _, err := db.ExecContext(context.Background(), `DELETE FROM `+table+` WHERE node_id LIKE $1`, prefix+"%"); err != nil {
			t.Fatalf("cleanup %s: %v", table, err)
		}
	}
}

func TestPostgresStoreContract(t *testing.T) {
	db := openNodesPostgres(t)
	prefix := fmt.Sprintf("pgt%d", time.Now().UnixNano())
	cleanupNodesPostgres(t, db, prefix)
	defer cleanupNodesPostgres(t, db, prefix)
	testStoreContract(t, NewPostgresStore(db), prefix)
}

// TestPostgresStoreNodeLimit fills a clean node table to MaxNodes; it skips on
// a database that already holds other nodes rather than disturb them.
func TestPostgresStoreNodeLimit(t *testing.T) {
	db := openNodesPostgres(t)
	ctx := context.Background()
	var existing int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(1) FROM gateway_helper_allocations) + (SELECT COUNT(1) FROM gateway_helper_registry)`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing != 0 {
		t.Skip("node tables are not empty; the cap test needs a clean database")
	}
	const prefix = "pgcap-"
	defer cleanupNodesPostgres(t, db, prefix)
	s := NewPostgresStore(db)
	for i := 0; i < MaxNodes; i++ {
		n := fmt.Sprintf("%s%03d", prefix, i)
		if err := s.ApplyAllocation(ctx, goodAlloc(n), t0); err != nil {
			t.Fatalf("allocation %d: %v", i, err)
		}
		if err := s.PutRegistry(ctx, RegistryEntry{NodeID: n, URISAN: NodeURISAN(n), SPKICurrent: pinA}, t0); err != nil {
			t.Fatalf("registry %d: %v", i, err)
		}
	}
	extra := prefix + "extra"
	if err := s.ApplyAllocation(ctx, goodAlloc(extra), t0); !errors.Is(err, ErrTooManyNodes) {
		t.Fatalf("allocation past the cap = %v", err)
	}
	if err := s.PutRegistry(ctx, RegistryEntry{NodeID: extra, URISAN: NodeURISAN(extra)}, t0); !errors.Is(err, ErrTooManyNodes) {
		t.Fatalf("registry past the cap = %v", err)
	}
	next := goodAlloc(prefix + "000")
	next.Generation++
	if err := s.ApplyAllocation(ctx, next, t0); err != nil {
		t.Fatalf("updating an existing node at the cap must work: %v", err)
	}
}

// TestPostgresStoreConcurrentGenerations applies every generation concurrently
// through separate store handles; the advisory lock and generation compare
// must leave the highest one standing.
func TestPostgresStoreConcurrentGenerations(t *testing.T) {
	db := openNodesPostgres(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("pgrace%d", time.Now().UnixNano())
	defer cleanupNodesPostgres(t, db, prefix)
	id := prefix + "-n"
	const top = 16
	errs := make(chan error, top+1)
	for g := 0; g <= top; g++ {
		go func(g int) {
			a := goodAlloc(id)
			a.Generation = int64((g * 7) % (top + 1)) // permutation of 0..top (7 is coprime with 17)
			a.MaxBrowserWorkloads = int(a.Generation % 8)
			err := NewPostgresStore(db).ApplyAllocation(ctx, a, t0)
			if errors.Is(err, ErrStaleGeneration) {
				err = nil
			}
			errs <- err
		}(g)
	}
	for g := 0; g <= top; g++ {
		if err := <-errs; err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	got, err := NewPostgresStore(db).GetAllocation(ctx, id)
	if err != nil || got.Generation != top {
		t.Fatalf("final generation = %d (%v), want %d", got.Generation, err, top)
	}
}
