// Package pat implements Personal Access Tokens (§11): opaque bearer tokens
// with the format ubag_pat_<base58(32 random bytes)>.
package pat

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const tokenPrefix = "ubag_pat_"

// Token is an issued personal-access token.
type Token struct {
	ID        string // opaque formatted token string
	TenantID  string
	AppID     string
	Role      string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// IsExpired reports whether t is past its expiry time as of now.
func (t Token) IsExpired(now time.Time) bool {
	return !t.ExpiresAt.IsZero() && now.After(t.ExpiresAt)
}

// Store persists PAT tokens.
type Store interface {
	Save(ctx context.Context, token Token) error
	Resolve(ctx context.Context, raw string, now time.Time) (Token, bool, error)
	Revoke(ctx context.Context, id string) error
}

// Issue generates a new PAT for the given tenant/app/role with the supplied TTL.
// A zero ttl means the token does not expire.
func Issue(tenantID, appID, role string, ttl time.Duration) (Token, error) {
	if strings.TrimSpace(tenantID) == "" {
		return Token{}, errors.New("pat: tenant_id is required")
	}
	if strings.TrimSpace(appID) == "" {
		return Token{}, errors.New("pat: app_id is required")
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return Token{}, fmt.Errorf("pat: generate random bytes: %w", err)
	}
	id := tokenPrefix + b58Encode(raw)
	now := time.Now().UTC()
	token := Token{
		ID:       id,
		TenantID: tenantID,
		AppID:    appID,
		Role:     role,
		IssuedAt: now,
	}
	if ttl > 0 {
		token.ExpiresAt = now.Add(ttl)
	}
	return token, nil
}

// IsValidFormat reports whether s looks like a PAT (has the expected prefix).
func IsValidFormat(s string) bool {
	return strings.HasPrefix(s, tokenPrefix) && len(s) > len(tokenPrefix)
}

// ─────────────────────────────────────────────────────────────────────────────
// MemoryStore
// ─────────────────────────────────────────────────────────────────────────────

// MemoryStore is an in-memory token store safe for concurrent use.
type MemoryStore struct {
	mu     sync.RWMutex
	tokens map[string]Token // keyed by token ID
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{tokens: make(map[string]Token)}
}

func (m *MemoryStore) Save(_ context.Context, token Token) error {
	if token.ID == "" {
		return errors.New("pat: token ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[token.ID] = token
	return nil
}

func (m *MemoryStore) Resolve(_ context.Context, raw string, now time.Time) (Token, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	token, ok := m.tokens[raw]
	if !ok {
		return Token{}, false, nil
	}
	if token.IsExpired(now) {
		return Token{}, false, nil
	}
	return token, true, nil
}

func (m *MemoryStore) Revoke(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, id)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// base58 encoder (Bitcoin alphabet, inline to avoid circular imports)
// ─────────────────────────────────────────────────────────────────────────────

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func b58Encode(input []byte) string {
	leadingZeros := 0
	for _, b := range input {
		if b != 0 {
			break
		}
		leadingZeros++
	}
	size := len(input)*2 + 1
	digits := make([]byte, size)
	digitsLen := 1
	for _, b := range input {
		carry := int(b)
		for i := 0; i < digitsLen; i++ {
			carry += int(digits[i]) << 8
			digits[i] = byte(carry % 58)
			carry /= 58
		}
		for carry > 0 {
			digits[digitsLen] = byte(carry % 58)
			digitsLen++
			carry /= 58
		}
	}
	out := make([]byte, leadingZeros+digitsLen)
	for i := 0; i < leadingZeros; i++ {
		out[i] = b58Alphabet[0]
	}
	for i := 0; i < digitsLen; i++ {
		out[leadingZeros+i] = b58Alphabet[digits[digitsLen-1-i]]
	}
	return string(out)
}
