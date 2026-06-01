// Package appjwt implements RS256 JSON Web Tokens for UBAG app credentials (§11).
//
// Tokens are compact JWTs signed with a 2048-bit RSA private key. The gateway
// validates them against the matching public key — no external JWT library is
// required.
package appjwt

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AppClaims is the payload carried inside an app JWT.
type AppClaims struct {
	TenantID string `json:"tid"`
	AppID    string `json:"sub"`
	Role     string `json:"role"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

// IsExpired reports whether the token is past its expiry as of now.
func (c AppClaims) IsExpired(now time.Time) bool {
	return now.Unix() > c.Expires
}

// KeySize is the RSA key size in bits.
const KeySize = 2048

// ErrExpired is returned by Verify when the token is past its expiry.
var ErrExpired = errors.New("appjwt: token is expired")

// ErrInvalid is returned by Verify for any structural or signature failure.
var ErrInvalid = errors.New("appjwt: invalid token")

// GenerateKeyPair generates a new 2048-bit RSA key pair.
func GenerateKeyPair() (*rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, KeySize)
	if err != nil {
		return nil, fmt.Errorf("appjwt: generate key: %w", err)
	}
	return key, nil
}

// Sign encodes claims into a compact RS256 JWT signed with privateKey.
func Sign(claims AppClaims, privateKey *rsa.PrivateKey) (string, error) {
	if privateKey == nil {
		return "", errors.New("appjwt: private key is required")
	}
	header := base64url(mustJSON(map[string]string{"alg": "RS256", "typ": "JWT"}))
	payload := base64url(mustJSON(claims))
	signingInput := header + "." + payload

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("appjwt: sign: %w", err)
	}
	return signingInput + "." + base64url(sig), nil
}

// Verify parses and validates a compact RS256 JWT, returning the embedded claims.
// Returns ErrExpired when the token has passed its exp claim, ErrInvalid for any
// other failure.
func Verify(tokenStr string, publicKey *rsa.PublicKey) (AppClaims, error) {
	if publicKey == nil {
		return AppClaims{}, fmt.Errorf("%w: public key is required", ErrInvalid)
	}
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) != 3 {
		return AppClaims{}, ErrInvalid
	}

	headerJSON, err := base64urlDecode(parts[0])
	if err != nil {
		return AppClaims{}, ErrInvalid
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil || header["alg"] != "RS256" {
		return AppClaims{}, fmt.Errorf("%w: unsupported algorithm", ErrInvalid)
	}

	// Verify signature.
	signingInput := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signingInput))
	sigBytes, err := base64urlDecode(parts[2])
	if err != nil {
		return AppClaims{}, ErrInvalid
	}
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], sigBytes); err != nil {
		return AppClaims{}, fmt.Errorf("%w: signature verification failed", ErrInvalid)
	}

	// Decode claims.
	payloadJSON, err := base64urlDecode(parts[1])
	if err != nil {
		return AppClaims{}, ErrInvalid
	}
	var claims AppClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return AppClaims{}, fmt.Errorf("%w: malformed claims", ErrInvalid)
	}

	if claims.Expires > 0 && time.Now().Unix() > claims.Expires {
		return AppClaims{}, ErrExpired
	}
	return claims, nil
}

// IssueToken is a convenience wrapper that builds claims and signs a token.
func IssueToken(tenantID, appID, role string, ttl time.Duration, privateKey *rsa.PrivateKey) (string, error) {
	now := time.Now().UTC()
	claims := AppClaims{
		TenantID: tenantID,
		AppID:    appID,
		Role:     role,
		IssuedAt: now.Unix(),
		Expires:  now.Add(ttl).Unix(),
	}
	return Sign(claims, privateKey)
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func base64url(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func base64urlDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("appjwt: marshal: " + err.Error())
	}
	return b
}
