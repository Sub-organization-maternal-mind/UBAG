package audit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newSQLiteAuditStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "audit.db") + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	store := NewSQLiteStore(db)
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("ready: %v", err)
	}
	return store
}

func sampleRecord(tenant, action, outcome string, at time.Time) Record {
	return Record{
		TenantID:   tenant,
		AppID:      "app-1",
		Actor:      "user@example.com",
		Action:     action,
		Resource:   "/v1/runs",
		Outcome:    outcome,
		OccurredAt: at,
		Attributes: map[string]any{"role": "operator", "decision": outcome},
	}
}

func appendChain(t *testing.T, store Store) []Record {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out []Record
	for i, action := range []string{"authorize", "authorize", "login"} {
		rec, err := store.Append(ctx, sampleRecord("tenant-1", action, "allow", base.Add(time.Duration(i)*time.Minute)))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		out = append(out, rec)
	}
	// A record for a different tenant must not appear in tenant-1's chain.
	if _, err := store.Append(ctx, sampleRecord("tenant-2", "authorize", "deny", base)); err != nil {
		t.Fatalf("append other tenant: %v", err)
	}
	return out
}

func runChainAssertions(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	appended := appendChain(t, store)

	if appended[0].PrevHash != GenesisHash {
		t.Errorf("first record PrevHash = %q, want genesis", appended[0].PrevHash)
	}
	for i := 1; i < len(appended); i++ {
		if appended[i].PrevHash != appended[i-1].RecordHash {
			t.Errorf("record %d PrevHash does not link to previous RecordHash", i)
		}
		if appended[i].Seq != appended[i-1].Seq+1 {
			t.Errorf("record %d Seq = %d, want %d", i, appended[i].Seq, appended[i-1].Seq+1)
		}
	}

	listed, err := store.List(ctx, Filter{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("listed %d records, want 3", len(listed))
	}
	if !VerifyChain(listed) {
		t.Fatalf("expected intact chain to verify")
	}

	head, headSeq, err := store.Head(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head != listed[len(listed)-1].RecordHash || headSeq != 3 {
		t.Errorf("head = (%q, %d), want (%q, 3)", head, headSeq, listed[len(listed)-1].RecordHash)
	}

	// Tamper: mutate a field in a listed record and confirm the chain fails.
	tampered := append([]Record(nil), listed...)
	tampered[1].Outcome = "deny"
	if VerifyChain(tampered) {
		t.Errorf("expected tampered chain to fail verification")
	}

	// Filter by time window.
	windowed, err := store.List(ctx, Filter{TenantID: "tenant-1", Since: listed[1].OccurredAt})
	if err != nil {
		t.Fatalf("list windowed: %v", err)
	}
	if len(windowed) != 2 {
		t.Errorf("windowed list returned %d records, want 2", len(windowed))
	}

	limited, err := store.List(ctx, Filter{TenantID: "tenant-1", Limit: 1})
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("limited list returned %d records, want 1", len(limited))
	}
}

func TestMemoryStoreChain(t *testing.T) {
	runChainAssertions(t, NewMemoryStore())
}

func TestSQLiteStoreChain(t *testing.T) {
	runChainAssertions(t, newSQLiteAuditStore(t))
}

func TestVerifyChainDetectsBrokenLink(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if _, err := store.Append(ctx, sampleRecord("t", "authorize", "allow", base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	listed, err := store.List(ctx, Filter{TenantID: "t"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	listed[1].PrevHash = "deadbeef"
	if VerifyChain(listed) {
		t.Errorf("expected broken prev_hash link to fail verification")
	}
}
