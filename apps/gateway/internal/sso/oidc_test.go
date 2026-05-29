package sso

import (
	"context"
	"errors"
	"testing"
	"time"
)

func baseOIDCConfig(pubPEM string) OIDCConfig {
	return OIDCConfig{
		Issuer:            "https://idp.example/",
		ClientID:          "ubag-gateway",
		ClientSecretRef:   "secretref://vault/oidc/ubag-gateway",
		JWKSPublicKeysPEM: []string{pubPEM},
		AllowedAudiences:  []string{"ubag-gateway"},
	}
}

func validClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":    "https://idp.example/",
		"aud":    "ubag-gateway",
		"sub":    "operator-123",
		"email":  "operator@example.com",
		"groups": []string{"admins", "ops"},
		"iat":    now.Add(-time.Minute).Unix(),
		"nbf":    now.Add(-time.Minute).Unix(),
		"exp":    now.Add(time.Hour).Unix(),
	}
}

func TestVerifyIDToken_Success(t *testing.T) {
	key, pubPEM, _ := testKeypair(t)
	now := time.Now().UTC()
	token := signJWT(t, key, map[string]any{"alg": "RS256", "typ": "JWT"}, validClaims(now))

	claims, err := VerifyIDToken(context.Background(), token, baseOIDCConfig(pubPEM), now)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if claims.Subject != "operator-123" {
		t.Errorf("sub = %q", claims.Subject)
	}
	if claims.Email != "operator@example.com" {
		t.Errorf("email = %q", claims.Email)
	}
	if len(claims.Groups) != 2 || claims.Groups[0] != "admins" {
		t.Errorf("groups = %v", claims.Groups)
	}
	if claims.Raw["iss"] != "https://idp.example/" {
		t.Errorf("raw iss = %v", claims.Raw["iss"])
	}
}

func TestVerifyIDToken_SuccessViaJWKS(t *testing.T) {
	key, _, _ := testKeypair(t)
	now := time.Now().UTC()
	token := signJWT(t, key, map[string]any{"alg": "RS256", "kid": "k1"}, validClaims(now))

	cfg := OIDCConfig{
		Issuer:           "https://idp.example/",
		AllowedAudiences: []string{"ubag-gateway"},
		JWKSJSON:         jwksFor(t, key, "k1"),
	}
	if _, err := VerifyIDToken(context.Background(), token, cfg, now); err != nil {
		t.Fatalf("expected JWKS verification success, got %v", err)
	}
}

func TestVerifyIDToken_Expired(t *testing.T) {
	key, pubPEM, _ := testKeypair(t)
	now := time.Now().UTC()
	claims := validClaims(now)
	claims["exp"] = now.Add(-time.Hour).Unix()
	token := signJWT(t, key, map[string]any{"alg": "RS256"}, claims)

	_, err := VerifyIDToken(context.Background(), token, baseOIDCConfig(pubPEM), now)
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestVerifyIDToken_WrongAudience(t *testing.T) {
	key, pubPEM, _ := testKeypair(t)
	now := time.Now().UTC()
	claims := validClaims(now)
	claims["aud"] = "some-other-client"
	token := signJWT(t, key, map[string]any{"alg": "RS256"}, claims)

	_, err := VerifyIDToken(context.Background(), token, baseOIDCConfig(pubPEM), now)
	if !errors.Is(err, ErrAudienceMismatch) {
		t.Fatalf("expected ErrAudienceMismatch, got %v", err)
	}
}

func TestVerifyIDToken_WrongIssuer(t *testing.T) {
	key, pubPEM, _ := testKeypair(t)
	now := time.Now().UTC()
	claims := validClaims(now)
	claims["iss"] = "https://evil.example/"
	token := signJWT(t, key, map[string]any{"alg": "RS256"}, claims)

	_, err := VerifyIDToken(context.Background(), token, baseOIDCConfig(pubPEM), now)
	if !errors.Is(err, ErrIssuerMismatch) {
		t.Fatalf("expected ErrIssuerMismatch, got %v", err)
	}
}

func TestVerifyIDToken_AlgNoneRejected(t *testing.T) {
	key, pubPEM, _ := testKeypair(t)
	now := time.Now().UTC()
	token := signJWT(t, key, map[string]any{"alg": "none"}, validClaims(now))

	_, err := VerifyIDToken(context.Background(), token, baseOIDCConfig(pubPEM), now)
	if !errors.Is(err, ErrUnsupportedAlg) {
		t.Fatalf("expected ErrUnsupportedAlg, got %v", err)
	}
}

func TestVerifyIDToken_BadSignature(t *testing.T) {
	signingKey, _, _ := testKeypair(t)
	_, otherPubPEM, _ := testKeypair(t)
	now := time.Now().UTC()
	token := signJWT(t, signingKey, map[string]any{"alg": "RS256"}, validClaims(now))

	// Verify against an unrelated public key -> signature must fail.
	_, err := VerifyIDToken(context.Background(), token, baseOIDCConfig(otherPubPEM), now)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid, got %v", err)
	}
}

func TestVerifyIDToken_TamperedPayload(t *testing.T) {
	key, pubPEM, _ := testKeypair(t)
	now := time.Now().UTC()
	token := signJWT(t, key, map[string]any{"alg": "RS256"}, validClaims(now))
	// Flip a character in the payload segment.
	tampered := token[:len(token)/2] + "X" + token[len(token)/2+1:]

	if _, err := VerifyIDToken(context.Background(), tampered, baseOIDCConfig(pubPEM), now); err == nil {
		t.Fatal("expected verification failure for tampered token")
	}
}
