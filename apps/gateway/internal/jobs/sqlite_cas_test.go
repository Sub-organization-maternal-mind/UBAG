package jobs

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteTransitionStatusHasSingleConcurrentWinner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	schema, err := os.ReadFile(filepath.Join("..", "sqlitestore", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	store := NewSQLiteStore(db)
	job, err := store.Create(context.Background(), CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant", AppID: "app",
		Target: "chatgpt_web", CommandType: "chat.prompt", Input: map[string]any{},
		AwaitingAttachments: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	errs := make([]error, 0, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, changed, err := store.TransitionStatus(context.Background(), job.ID, StatusCreated, StatusQueued)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if changed {
				winners++
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("transition errors: %v", errs)
	}
	if winners != 1 {
		t.Fatalf("CAS winners = %d, want exactly 1", winners)
	}
}

func TestSQLiteTransitionStatusUsesConditionalUpdate(t *testing.T) {
	source, err := os.ReadFile("sqlite.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "WHERE id = ? AND status = ?") {
		t.Fatal("TransitionStatus must perform UPDATE ... WHERE id = ? AND status = ?")
	}
}
