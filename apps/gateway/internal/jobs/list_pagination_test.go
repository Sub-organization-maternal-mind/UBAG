package jobs

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSQLiteListCursorAndLimit pins the store-side pagination contract of
// List: the (created_at, id) row-value cursor is exclusive, Descending flips
// both the ordering and the cursor direction, and Limit caps the page — so a
// list route never loads the tenant's whole job table to render one page.
func TestSQLiteListCursorAndLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema, err := os.ReadFile(filepath.Join("..", "sqlitestore", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	store := NewSQLiteStore(db)
	ctx := context.Background()

	ids := make([]string, 0, 5)
	for range 5 {
		job, err := store.Create(ctx, CreateRequest{
			APIVersion: "2026-05-22", TenantID: "tenant_page", AppID: "app_page",
			Target: "mock", CommandType: "submit", Input: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
	}

	ascending := func(filter ListFilter) []string {
		t.Helper()
		jobs, err := store.List(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(jobs))
		for _, job := range jobs {
			out = append(out, job.ID)
		}
		return out
	}

	// Ascending order, limited.
	if got := ascending(ListFilter{TenantID: "tenant_page", Limit: 2}); len(got) != 2 || got[0] != ids[0] || got[1] != ids[1] {
		t.Fatalf("ascending limited page = %v, want %v", got, ids[:2])
	}
	// Exclusive cursor continues strictly after the cursor job.
	if got := ascending(ListFilter{TenantID: "tenant_page", AfterID: ids[1], Limit: 2}); len(got) != 2 || got[0] != ids[2] || got[1] != ids[3] {
		t.Fatalf("ascending cursor page = %v, want %v", got, ids[2:4])
	}
	// Descending order, limited, cursor walking older.
	if got := ascending(ListFilter{TenantID: "tenant_page", Descending: true, Limit: 2}); len(got) != 2 || got[0] != ids[4] || got[1] != ids[3] {
		t.Fatalf("descending limited page = %v, want %v", got, []string{ids[4], ids[3]})
	}
	if got := ascending(ListFilter{TenantID: "tenant_page", Descending: true, AfterID: ids[3], Limit: 2}); len(got) != 2 || got[0] != ids[2] || got[1] != ids[1] {
		t.Fatalf("descending cursor page = %v, want %v", got, []string{ids[2], ids[1]})
	}
	// A page that ends at the table edge reports no extra rows.
	if got := ascending(ListFilter{TenantID: "tenant_page", AfterID: ids[4], Limit: 2}); len(got) != 0 {
		t.Fatalf("page past the end = %v, want empty", got)
	}
}
