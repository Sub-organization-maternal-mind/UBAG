// Package session provides server-side SSO session management for the UBAG
// gateway. A session is keyed by an opaque, high-entropy bearer token minted on
// successful SSO login. Only the SHA-256 hash of the token is persisted, so a
// leak of the store never reveals usable tokens. Tokens carry no claims; the
// mapped Principal is resolved from the store on every request.
//
// Three backends mirror the gateway's store conventions: in-memory (default /
// tests), SQLite, and Postgres.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// tokenBytes is the number of random bytes in a session token (256 bits).
const tokenBytes = 32

// ErrNotConfigured is returned when a backend is used without a database.
var ErrNotConfigured = errors.New("session: store is not configured")

// Session is a server-side session and its mapped principal.
type Session struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	AppID     string    `json:"app_id"`
	Role      string    `json:"role"`
	Subject   string    `json:"subject"`
	Email     string    `json:"email,omitempty"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

// Store mints, resolves, and revokes sessions.
type Store interface {
	Ready(ctx context.Context) error
	// Create persists a session for the given principal fields (TenantID,
	// AppID, Role, Subject, Email, IssuedAt, ExpiresAt) and returns the stored
	// session plus the plaintext bearer token. The token is returned only here;
	// the store retains only its hash.
	Create(ctx context.Context, sess Session) (Session, string, error)
	// Resolve returns the live session for a presented token. ok is false when
	// the token is unknown, revoked, or expired as of now.
	Resolve(ctx context.Context, token string, now time.Time) (Session, bool, error)
	// Revoke marks the session for a presented token as revoked. It returns
	// true when a matching, not-already-revoked session was found. Revoking an
	// unknown or already-revoked token is a no-op that returns false.
	Revoke(ctx context.Context, token string, now time.Time) (bool, error)
}

// generateToken returns a URL-safe, unpadded base64 token with tokenBytes of
// cryptographic randomness.
func generateToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("session: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// hashToken returns the hex-encoded SHA-256 of a token, used as the store key.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newSessionID(hash string) string {
	return "sess_" + hash[:24]
}

func normalize(sess *Session) {
	if sess.IssuedAt.IsZero() {
		sess.IssuedAt = time.Now()
	}
	sess.IssuedAt = sess.IssuedAt.UTC().Truncate(time.Second)
	sess.ExpiresAt = sess.ExpiresAt.UTC().Truncate(time.Second)
	if sess.Role == "" {
		sess.Role = "viewer"
	}
}

func isLive(sess Session, now time.Time) bool {
	if sess.Revoked {
		return false
	}
	if !sess.ExpiresAt.IsZero() && !now.Before(sess.ExpiresAt) {
		return false
	}
	return true
}

// MemoryStore is an in-memory Store keyed by token hash.
type MemoryStore struct {
	mu      sync.Mutex
	byHash map[string]Session
}

// NewMemoryStore returns an empty in-memory session store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byHash: make(map[string]Session)}
}

func (m *MemoryStore) Ready(context.Context) error { return nil }

func (m *MemoryStore) Create(_ context.Context, sess Session) (Session, string, error) {
	normalize(&sess)
	token, err := generateToken()
	if err != nil {
		return Session{}, "", err
	}
	hash := hashToken(token)
	sess.ID = newSessionID(hash)
	sess.Revoked = false

	m.mu.Lock()
	m.byHash[hash] = sess
	m.mu.Unlock()
	return sess, token, nil
}

func (m *MemoryStore) Resolve(_ context.Context, token string, now time.Time) (Session, bool, error) {
	if token == "" {
		return Session{}, false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.byHash[hashToken(token)]
	if !ok || !isLive(sess, now) {
		return Session{}, false, nil
	}
	return sess, true, nil
}

func (m *MemoryStore) Revoke(_ context.Context, token string, now time.Time) (bool, error) {
	if token == "" {
		return false, nil
	}
	hash := hashToken(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.byHash[hash]
	if !ok || sess.Revoked {
		return false, nil
	}
	sess.Revoked = true
	m.byHash[hash] = sess
	return true, nil
}
