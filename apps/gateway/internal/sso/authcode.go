package sso

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Sentinel errors for the authorization-code flow.
var (
	// ErrStateMismatch is returned when the state parameter is absent,
	// does not match any pending request, or has expired.
	ErrStateMismatch = errors.New("sso: state parameter does not match or has expired")
	// ErrCodeExchange is returned when the code→token HTTP exchange fails.
	ErrCodeExchange = errors.New("sso: authorization code exchange failed")
	// ErrMissingIDToken is returned when the token endpoint response does not
	// include an id_token field.
	ErrMissingIDToken = errors.New("sso: token response missing id_token")
)

// defaultStateTTL is the lifetime of a pending state entry. After this window
// the Consume call returns ok=false, preventing replay of stale states.
const defaultStateTTL = 10 * time.Minute

// stateEntry is the value held in MemoryStateStore for a pending request.
type stateEntry struct {
	nonce     string
	createdAt time.Time
}

// StateStore tracks pending OIDC authorization-code requests by state value.
type StateStore interface {
	// Set stores state→{nonce, createdAt} for TTL-based expiry.
	Set(state, nonce string) error
	// Consume retrieves and removes the nonce for state.
	// ok=false if not found or expired.
	Consume(state string) (nonce string, ok bool)
}

// MemoryStateStore is an in-memory StateStore with TTL-based expiry.
// It is safe for concurrent use.
type MemoryStateStore struct {
	mu      sync.Mutex
	entries map[string]stateEntry
	ttl     time.Duration
}

// NewMemoryStateStore creates a MemoryStateStore that expires entries after ttl.
// When ttl is zero, defaultStateTTL (10 minutes) is used.
func NewMemoryStateStore(ttl time.Duration) *MemoryStateStore {
	if ttl <= 0 {
		ttl = defaultStateTTL
	}
	return &MemoryStateStore{
		entries: make(map[string]stateEntry),
		ttl:     ttl,
	}
}

// Set stores state→nonce. An existing entry for the same state is overwritten.
func (s *MemoryStateStore) Set(state, nonce string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[state] = stateEntry{
		nonce:     nonce,
		createdAt: time.Now().UTC(),
	}
	return nil
}

// Consume atomically retrieves and removes the nonce for state.
// Returns ok=false when the state is unknown or the TTL has elapsed.
func (s *MemoryStateStore) Consume(state string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[state]
	if !exists {
		return "", false
	}
	delete(s.entries, state)
	if time.Since(entry.createdAt) > s.ttl {
		return "", false
	}
	return entry.nonce, true
}

// TokenExchanger performs the OAuth2 authorization-code exchange.
type TokenExchanger interface {
	// Exchange trades an authorization code for tokens via the token endpoint.
	// It returns the raw id_token and access_token strings on success.
	Exchange(ctx context.Context, tokenURL, code, clientID, clientSecret, redirectURI string) (idToken, accessToken string, err error)
}

// tokenResponse is the JSON shape returned by an OIDC token endpoint.
type tokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// HTTPExchanger is the real TokenExchanger that calls the IdP token endpoint
// using net/http with application/x-www-form-urlencoded encoding.
type HTTPExchanger struct{}

// Exchange POSTs grant_type=authorization_code to tokenURL and returns the
// id_token and access_token from the JSON response.
func (h *HTTPExchanger) Exchange(
	ctx context.Context,
	tokenURL, code, clientID, clientSecret, redirectURI string,
) (idToken, accessToken string, err error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("%w: build request: %v", ErrCodeExchange, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: http: %v", ErrCodeExchange, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", fmt.Errorf("%w: read response: %v", ErrCodeExchange, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%w: token endpoint returned %d: %s", ErrCodeExchange, resp.StatusCode, string(body))
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", "", fmt.Errorf("%w: decode response: %v", ErrCodeExchange, err)
	}
	if tok.IDToken == "" {
		return "", "", ErrMissingIDToken
	}
	return tok.IDToken, tok.AccessToken, nil
}

// AuthCodeFlow manages the OIDC authorization-code flow end-to-end.
// It is safe for concurrent use.
type AuthCodeFlow struct {
	states    StateStore
	Exchanger TokenExchanger
}

// NewAuthCodeFlow returns an AuthCodeFlow backed by an in-memory state store
// with the default 10-minute TTL and the real HTTP token exchanger.
func NewAuthCodeFlow() *AuthCodeFlow {
	return &AuthCodeFlow{
		states:    NewMemoryStateStore(defaultStateTTL),
		Exchanger: &HTTPExchanger{},
	}
}

// NewAuthCodeFlowWithStore returns an AuthCodeFlow using the supplied StateStore
// and Exchanger. Intended for testing.
func NewAuthCodeFlowWithStore(store StateStore, exchanger TokenExchanger) *AuthCodeFlow {
	return &AuthCodeFlow{
		states:    store,
		Exchanger: exchanger,
	}
}

// GenerateState creates a cryptographically random 32-byte state token, stores
// it together with nonce in the StateStore, and returns the state string.
func (f *AuthCodeFlow) GenerateState(nonce string) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("sso: generate state: %w", err)
	}
	state := hex.EncodeToString(raw)
	if err := f.states.Set(state, nonce); err != nil {
		return "", fmt.Errorf("sso: store state: %w", err)
	}
	return state, nil
}

// ValidateCallback verifies the state token (consuming it from the store) and
// returns the nonce that was stored when the flow was initiated. It returns
// ErrStateMismatch when state is empty, unknown, or expired.
func (f *AuthCodeFlow) ValidateCallback(state, code string) (string, error) {
	if state == "" {
		return "", ErrStateMismatch
	}
	if code == "" {
		return "", ErrStateMismatch
	}
	nonce, ok := f.states.Consume(state)
	if !ok {
		return "", ErrStateMismatch
	}
	return nonce, nil
}
