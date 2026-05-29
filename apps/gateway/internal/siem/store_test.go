package siem

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func runConfigStoreSuite(t *testing.T, store ConfigStore) {
	t.Helper()
	ctx := context.Background()
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}

	// Create.
	created, err := store.Put(ctx, SinkConfig{
		TenantID:  "tenant-a",
		Name:      "primary-http",
		Kind:      "http",
		Target:    "https://siem.example.com/ingest",
		SecretRef: "secret-ref-123",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected generated id")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("expected timestamps populated")
	}

	// Read.
	got, ok, err := store.Get(ctx, "tenant-a", created.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.SecretRef != "secret-ref-123" {
		t.Fatalf("secret ref not persisted: %q", got.SecretRef)
	}
	if got.Target != "https://siem.example.com/ingest" || got.Kind != "http" {
		t.Fatalf("unexpected config: %+v", got)
	}

	// Update.
	got.Enabled = false
	got.Name = "renamed"
	updated, err := store.Put(ctx, got)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Enabled {
		t.Fatal("expected enabled=false after update")
	}
	if updated.CreatedAt != created.CreatedAt {
		t.Fatalf("created_at should be preserved on update: %v vs %v", updated.CreatedAt, created.CreatedAt)
	}

	// Scope isolation: a different tenant must not see tenant-a config.
	if _, ok, err := store.Get(ctx, "tenant-b", created.ID); err != nil || ok {
		t.Fatalf("expected scope isolation, ok=%v err=%v", ok, err)
	}
	if _, err := store.Put(ctx, SinkConfig{TenantID: "tenant-b", Kind: "syslog", Target: "127.0.0.1:514", Network: "udp"}); err != nil {
		t.Fatalf("put tenant-b: %v", err)
	}
	listA, err := store.List(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("list a: %v", err)
	}
	if len(listA) != 1 {
		t.Fatalf("expected 1 config for tenant-a, got %d", len(listA))
	}
	listB, err := store.List(ctx, "tenant-b")
	if err != nil {
		t.Fatalf("list b: %v", err)
	}
	if len(listB) != 1 {
		t.Fatalf("expected 1 config for tenant-b, got %d", len(listB))
	}

	// Delete.
	ok, err = store.Delete(ctx, "tenant-a", created.ID)
	if err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := store.Get(ctx, "tenant-a", created.ID); ok {
		t.Fatal("expected config gone after delete")
	}
	if ok, _ := store.Delete(ctx, "tenant-a", created.ID); ok {
		t.Fatal("expected delete of missing config to report false")
	}
}

func TestMemoryConfigStore(t *testing.T) {
	runConfigStoreSuite(t, NewMemoryStore())
}

func openTestSQLite(t *testing.T) *sql.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "siem-config.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSQLiteConfigStore(t *testing.T) {
	runConfigStoreSuite(t, NewSQLiteStore(openTestSQLite(t)))
}

func TestSQLiteConfigStoreNoRawSecretPersisted(t *testing.T) {
	db := openTestSQLite(t)
	store := NewSQLiteStore(db)
	ctx := context.Background()
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}
	const rawSecret = "DO-NOT-PERSIST-THIS-RAW-SECRET"
	cfg, err := store.Put(ctx, SinkConfig{
		TenantID:  "tenant-a",
		Kind:      "http",
		Target:    "https://siem.example.com/ingest",
		SecretRef: "ref-only-789",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Scan every text column and assert the raw secret never appears.
	rows, err := db.QueryContext(ctx, `SELECT id, tenant_id, name, kind, target, network, secret_ref FROM gateway_siem_sink_configs WHERE id = ?`, cfg.ID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, tenant, name, kind, target, network, secretRef string
		if err := rows.Scan(&id, &tenant, &name, &kind, &target, &network, &secretRef); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, value := range []string{id, tenant, name, kind, target, network, secretRef} {
			if value == rawSecret {
				t.Fatalf("raw secret leaked into column value %q", value)
			}
		}
		if secretRef != "ref-only-789" {
			t.Fatalf("expected reference stored, got %q", secretRef)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
}
