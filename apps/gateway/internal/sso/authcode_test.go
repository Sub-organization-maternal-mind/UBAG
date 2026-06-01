package sso

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// MemoryStateStore tests
// ---------------------------------------------------------------------------

func TestMemoryStateStoreSetAndConsume(t *testing.T) {
	store := NewMemoryStateStore(time.Minute)

	if err := store.Set("state-abc", "nonce-xyz"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// First consume should return the nonce.
	nonce, ok := store.Consume("state-abc")
	if !ok {
		t.Fatal("Consume returned ok=false, want true")
	}
	if nonce != "nonce-xyz" {
		t.Fatalf("nonce = %q, want %q", nonce, "nonce-xyz")
	}

	// Second consume of the same state must return not-found.
	_, ok = store.Consume("state-abc")
	if ok {
		t.Fatal("second Consume returned ok=true; entry should have been deleted")
	}
}

func TestMemoryStateStoreNotFound(t *testing.T) {
	store := NewMemoryStateStore(time.Minute)
	_, ok := store.Consume("nonexistent-state")
	if ok {
		t.Fatal("Consume of unknown state returned ok=true")
	}
}

func TestMemoryStateStoreExpiry(t *testing.T) {
	// Use a very short TTL so the entry expires immediately.
	store := NewMemoryStateStore(time.Nanosecond)

	if err := store.Set("state-exp", "nonce-exp"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Sleep long enough that even a nanosecond TTL is exceeded.
	time.Sleep(5 * time.Millisecond)

	_, ok := store.Consume("state-exp")
	if ok {
		t.Fatal("Consume returned ok=true for an expired entry")
	}
}

func TestStateStoreConcurrency(t *testing.T) {
	store := NewMemoryStateStore(time.Minute)
	const workers = 50

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	// Concurrent Sets.
	for i := range workers {
		state := "state-" + string(rune('A'+i))
		nonce := "nonce-" + string(rune('A'+i))
		go func(st, nc string) {
			defer wg.Done()
			_ = store.Set(st, nc)
		}(state, nonce)
	}

	// Concurrent Consumes — some will hit, some miss; we only care no race occurs.
	for i := range workers {
		state := "state-" + string(rune('A'+i))
		go func(st string) {
			defer wg.Done()
			store.Consume(st)
		}(state)
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// AuthCodeFlow tests
// ---------------------------------------------------------------------------

func TestGenerateStateRandomness(t *testing.T) {
	flow := NewAuthCodeFlow()

	state1, err := flow.GenerateState("nonce-1")
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	state2, err := flow.GenerateState("nonce-2")
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}

	if state1 == state2 {
		t.Fatal("two GenerateState calls produced the same state token")
	}
	if len(state1) < 32 {
		t.Fatalf("state too short: %q (len=%d)", state1, len(state1))
	}
}

func TestValidateCallbackStateMismatch(t *testing.T) {
	flow := NewAuthCodeFlow()

	// No prior Set — wrong state.
	_, err := flow.ValidateCallback("bad-state", "code-abc")
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("expected ErrStateMismatch, got %v", err)
	}
}

func TestValidateCallbackEmptyState(t *testing.T) {
	flow := NewAuthCodeFlow()
	_, err := flow.ValidateCallback("", "some-code")
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("expected ErrStateMismatch for empty state, got %v", err)
	}
}

func TestValidateCallbackEmptyCode(t *testing.T) {
	flow := NewAuthCodeFlow()
	// Even if state was stored, missing code should fail.
	state, _ := flow.GenerateState("nonce-x")
	_, err := flow.ValidateCallback(state, "")
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("expected ErrStateMismatch for empty code, got %v", err)
	}
}

func TestValidateCallbackSuccess(t *testing.T) {
	flow := NewAuthCodeFlow()

	state, err := flow.GenerateState("my-nonce")
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}

	nonce, err := flow.ValidateCallback(state, "exchange-code")
	if err != nil {
		t.Fatalf("ValidateCallback: %v", err)
	}
	if nonce != "my-nonce" {
		t.Fatalf("nonce = %q, want %q", nonce, "my-nonce")
	}
}

// ---------------------------------------------------------------------------
// HTTPExchanger mock-server test
// ---------------------------------------------------------------------------

func TestHTTPExchangerMock(t *testing.T) {
	// Token endpoint mock: returns a valid JSON token response.
	wantIDToken := "header.payload.sig"
	wantAccessToken := "access-tok-123"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "authorization_code" {
			http.Error(w, "bad grant_type", http.StatusBadRequest)
			return
		}
		if r.FormValue("code") != "test-code" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{
			IDToken:     wantIDToken,
			AccessToken: wantAccessToken,
			TokenType:   "Bearer",
		})
	}))
	defer srv.Close()

	ex := &HTTPExchanger{}
	idTok, accTok, err := ex.Exchange(context.Background(), srv.URL, "test-code", "client-id", "secret", "https://example.com/cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if idTok != wantIDToken {
		t.Errorf("id_token = %q, want %q", idTok, wantIDToken)
	}
	if accTok != wantAccessToken {
		t.Errorf("access_token = %q, want %q", accTok, wantAccessToken)
	}
}

func TestHTTPExchangerMissingIDToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Respond with a token body that lacks id_token.
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "acc",
			"token_type":   "Bearer",
		})
	}))
	defer srv.Close()

	ex := &HTTPExchanger{}
	_, _, err := ex.Exchange(context.Background(), srv.URL, "code", "cid", "sec", "")
	if !errors.Is(err, ErrMissingIDToken) {
		t.Fatalf("expected ErrMissingIDToken, got %v", err)
	}
}

func TestHTTPExchangerNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	ex := &HTTPExchanger{}
	_, _, err := ex.Exchange(context.Background(), srv.URL, "bad-code", "cid", "sec", "")
	if !errors.Is(err, ErrCodeExchange) {
		t.Fatalf("expected ErrCodeExchange, got %v", err)
	}
}
