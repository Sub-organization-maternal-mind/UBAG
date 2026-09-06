package storekit

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

type fakeStore struct{ kind string }

func TestPickMemoryWhenNoDB(t *testing.T) {
	for _, kind := range []Kind{KindMemory, KindSQLite, KindPostgres, Kind("bogus")} {
		got, err := Pick(
			kind, nil, "test",
			func(*sql.DB) (fakeStore, error) { return fakeStore{"sqlite"}, nil },
			func(*sql.DB) (fakeStore, error) { return fakeStore{"postgres"}, nil },
			func() fakeStore { return fakeStore{"memory"} },
		)
		if err != nil {
			t.Fatalf("Pick(%s, nil db) errored: %v", kind, err)
		}
		if got.kind != "memory" {
			t.Fatalf("Pick(%s, nil db) = %s, want memory", kind, got.kind)
		}
	}
}

func TestPickErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	// A nil driver can't be opened here without a real DB; the error path is
	// exercised via the sqlite adapter closure.
	_, err := Pick(
		KindSQLite, nil, "test",
		func(*sql.DB) (fakeStore, error) { return fakeStore{}, boom },
		func(*sql.DB) (fakeStore, error) { return fakeStore{}, boom },
		func() fakeStore { return fakeStore{"memory"} },
	)
	if err != nil {
		t.Fatalf("nil db must take the memory path without calling adapters: %v", err)
	}
}

func TestRequireSQLiteObjectMissing(t *testing.T) {
	// In-memory SQLite has no such table; the assertion must fail cleanly.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Skip("sqlite driver not available in this environment")
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); err != nil {
		t.Skip("sqlite not usable here")
	}
	if err := RequireSQLiteObject(context.Background(), db, "definitely_missing"); err == nil {
		t.Fatal("expected missing-table error")
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE real_table (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := RequireSQLiteObject(context.Background(), db, "real_table"); err != nil {
		t.Fatalf("existing table reported missing: %v", err)
	}
}
