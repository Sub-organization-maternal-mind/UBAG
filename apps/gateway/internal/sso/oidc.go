package sso

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// clockSkew is the small leeway allowed when comparing exp/nbf/iat against now
// to tolerate minor clock drift between the IdP and the gateway.
const clockSkew = 60 * time.Second

var (
	// ErrTokenMalformed indicates the raw JWT could not be parsed.
	ErrTokenMalformed = errors.New("sso: id token is malformed")
	// ErrUnsupportedAlg indicates the JWT used an algorithm other than RS256
	// (notably "none", which is always rejected).
	ErrUnsupportedAlg = errors.New("sso: id token uses an unsupported alg (only RS256 is accepted)")
	// ErrSignatureInvalid indicates no configured key verified the signature.
	ErrSignatureInvalid = errors.New("sso: id token signature is invalid")
	// ErrIssuerMismatch indicates the iss claim did not match the configured issuer.
	ErrIssuerMismatch = errors.New("sso: id token issuer mismatch")
	// ErrAudienceMismatch indicates the aud claim did not intersect the allowed audiences.
	ErrAudienceMismatch = errors.New("sso: id token audience mismatch")
	// ErrTokenExpired indicates the token's exp is in the past.
	ErrTokenExpired = errors.New("sso: id token is expired")
	// ErrTokenNotYetValid indicates the token's nbf/iat is in the future.
	ErrTokenNotYetValid = errors.New("sso: id token is not yet valid")
	// ErrNoVerificationKeys indicates the OIDC config carried no usable public keys.
	ErrNoVerificationKeys = errors.New("sso: oidc config has no RSA verification keys")
)

type rsaKey struct {
	kid string
	pub *rsa.PublicKey
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// VerifyIDToken verifies an OIDC ID token (a compact RS256 JWT) against cfg and
// returns the parsed claims. The verification performs, in order:
//
//   - structural parse of the three base64url segments;
//   - rejection of any alg other than RS256 (including "none");
//   - RSA-PKCS1v15 / SHA-256 signature verification against one of the
//     configured public keys (PEM and/or JWKS), matching kid when present;
//   - iss equality against cfg.Issuer;
//   - aud intersection against cfg.AllowedAudiences;
//   - exp / nbf / iat validation against now (with a small clock-skew leeway).
func VerifyIDToken(ctx context.Context, rawJWT string, cfg OIDCConfig, now time.Time) (Claims, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Claims{}, err
		}
	}
	now = now.UTC()

	segments := strings.Split(strings.TrimSpace(rawJWT), ".")
	if len(segments) != 3 {
		return Claims{}, ErrTokenMalformed
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: header: %v", ErrTokenMalformed, err)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: payload: %v", ErrTokenMalformed, err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(segments[2])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: signature: %v", ErrTokenMalformed, err)
	}

	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return Claims{}, fmt.Errorf("%w: header json: %v", ErrTokenMalformed, err)
	}
	if !strings.EqualFold(header.Alg, "RS256") {
		return Claims{}, fmt.Errorf("%w: %q", ErrUnsupportedAlg, header.Alg)
	}

	keys, err := cfg.verificationKeys()
	if err != nil {
		return Claims{}, err
	}
	if len(keys) == 0 {
		return Claims{}, ErrNoVerificationKeys
	}

	signingInput := segments[0] + "." + segments[1]
	digest := sha256.Sum256([]byte(signingInput))
	if !verifyAgainstKeys(keys, header.Kid, digest[:], signature) {
		return Claims{}, ErrSignatureInvalid
	}

	var raw map[string]any
	if err := json.Unmarshal(payloadBytes, &raw); err != nil {
		return Claims{}, fmt.Errorf("%w: payload json: %v", ErrTokenMalformed, err)
	}

	claims := Claims{
		Subject:   stringClaim(raw, "sub"),
		Email:     stringClaim(raw, "email"),
		Groups:    valueToStrings(raw["groups"]),
		Issuer:    stringClaim(raw, "iss"),
		Audience:  valueToStrings(raw["aud"]),
		IssuedAt:  unixClaim(raw, "iat"),
		Expiry:    unixClaim(raw, "exp"),
		NotBefore: unixClaim(raw, "nbf"),
		Raw:       raw,
	}

	if cfg.Issuer != "" && claims.Issuer != cfg.Issuer {
		return Claims{}, fmt.Errorf("%w: got %q want %q", ErrIssuerMismatch, claims.Issuer, cfg.Issuer)
	}
	if len(cfg.AllowedAudiences) > 0 && !intersects(claims.Audience, cfg.AllowedAudiences) {
		return Claims{}, ErrAudienceMismatch
	}
	if !claims.Expiry.IsZero() && now.After(claims.Expiry.Add(clockSkew)) {
		return Claims{}, ErrTokenExpired
	}
	if !claims.NotBefore.IsZero() && now.Add(clockSkew).Before(claims.NotBefore) {
		return Claims{}, ErrTokenNotYetValid
	}
	if !claims.IssuedAt.IsZero() && now.Add(clockSkew).Before(claims.IssuedAt) {
		return Claims{}, ErrTokenNotYetValid
	}
	return claims, nil
}

func verifyAgainstKeys(keys []rsaKey, kid string, digest, signature []byte) bool {
	// When the token advertises a kid, prefer keys that match it; fall back to
	// trying every key if no kid match verifies (some IdPs omit kid in JWKS).
	if kid != "" {
		for _, key := range keys {
			if key.kid == kid && rsa.VerifyPKCS1v15(key.pub, crypto.SHA256, digest, signature) == nil {
				return true
			}
		}
	}
	for _, key := range keys {
		if rsa.VerifyPKCS1v15(key.pub, crypto.SHA256, digest, signature) == nil {
			return true
		}
	}
	return false
}

// verificationKeys parses the configured PEM keys and JWKS blob into RSA public
// keys. It never returns or logs any private key material.
func (cfg OIDCConfig) verificationKeys() ([]rsaKey, error) {
	keys := []rsaKey{}
	for _, pemStr := range cfg.JWKSPublicKeysPEM {
		pub, err := parseRSAPublicKeyPEM(pemStr)
		if err != nil {
			return nil, err
		}
		keys = append(keys, rsaKey{pub: pub})
	}
	if len(cfg.JWKSJSON) > 0 {
		jwksKeys, err := parseJWKS(cfg.JWKSJSON)
		if err != nil {
			return nil, err
		}
		keys = append(keys, jwksKeys...)
	}
	return keys, nil
}

func parseRSAPublicKeyPEM(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("sso: invalid PEM block for public key")
	}
	switch block.Type {
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("sso: certificate does not contain an RSA public key")
		}
		return pub, nil
	case "PUBLIC KEY":
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("sso: PEM public key is not RSA")
		}
		return pub, nil
	case "RSA PUBLIC KEY":
		return x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return nil, fmt.Errorf("sso: unsupported PEM block type %q", block.Type)
	}
}

type jwksDocument struct {
	Keys []jwksKey `json:"keys"`
}

type jwksKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func parseJWKS(blob []byte) ([]rsaKey, error) {
	var doc jwksDocument
	if err := json.Unmarshal(blob, &doc); err != nil {
		return nil, fmt.Errorf("sso: invalid JWKS json: %w", err)
	}
	keys := []rsaKey{}
	for _, jwk := range doc.Keys {
		if !strings.EqualFold(jwk.Kty, "RSA") {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
		if err != nil {
			return nil, fmt.Errorf("sso: invalid JWKS modulus: %w", err)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil {
			return nil, fmt.Errorf("sso: invalid JWKS exponent: %w", err)
		}
		pub := &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(new(big.Int).SetBytes(eBytes).Int64()),
		}
		if pub.E == 0 {
			return nil, errors.New("sso: JWKS key has zero exponent")
		}
		keys = append(keys, rsaKey{kid: jwk.Kid, pub: pub})
	}
	return keys, nil
}

func stringClaim(raw map[string]any, key string) string {
	if value, ok := raw[key]; ok {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

func unixClaim(raw map[string]any, key string) time.Time {
	value, ok := raw[key]
	if !ok {
		return time.Time{}
	}
	switch number := value.(type) {
	case float64:
		return time.Unix(int64(number), 0).UTC()
	case json.Number:
		if seconds, err := number.Int64(); err == nil {
			return time.Unix(seconds, 0).UTC()
		}
	}
	return time.Time{}
}

func valueToStrings(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return []string{typed}
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if str, ok := item.(string); ok {
				out = append(out, str)
			} else {
				out = append(out, fmt.Sprint(item))
			}
		}
		return out
	case float64:
		return []string{formatNumber(typed)}
	case bool:
		return []string{fmt.Sprint(typed)}
	default:
		return []string{fmt.Sprint(typed)}
	}
}

func formatNumber(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%v", value)
}

func intersects(left, right []string) bool {
	set := make(map[string]struct{}, len(right))
	for _, item := range right {
		set[item] = struct{}{}
	}
	for _, item := range left {
		if _, ok := set[item]; ok {
			return true
		}
	}
	return false
}
