package appjwt

import (
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	token, err := IssueToken("tenant1", "app1", "admin", time.Hour, priv)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if strings.Count(token, ".") != 2 {
		t.Errorf("token must have 3 parts: %q", token)
	}

	claims, err := Verify(token, &priv.PublicKey)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.TenantID != "tenant1" || claims.AppID != "app1" || claims.Role != "admin" {
		t.Errorf("claims mismatch: %+v", claims)
	}
}

func TestVerifyWrongKey(t *testing.T) {
	priv1, _ := GenerateKeyPair()
	priv2, _ := GenerateKeyPair()

	token, _ := IssueToken("t", "a", "viewer", time.Hour, priv1)
	if _, err := Verify(token, &priv2.PublicKey); err == nil {
		t.Error("Verify must fail with the wrong public key")
	}
}

func TestVerifyExpired(t *testing.T) {
	priv, _ := GenerateKeyPair()
	// Issue a token that expired 1 second ago.
	claims := AppClaims{
		TenantID: "t", AppID: "a", Role: "viewer",
		IssuedAt: time.Now().Add(-2 * time.Second).Unix(),
		Expires:  time.Now().Add(-time.Second).Unix(),
	}
	token, _ := Sign(claims, priv)
	_, err := Verify(token, &priv.PublicKey)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("Verify must return ErrExpired for expired token, got: %v", err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	priv, _ := GenerateKeyPair()
	for _, bad := range []string{"", "a", "a.b", "a.b.c.d"} {
		if _, err := Verify(bad, &priv.PublicKey); err == nil {
			t.Errorf("Verify(%q) must fail", bad)
		}
	}
}

func TestVerifyTamperedPayload(t *testing.T) {
	priv, _ := GenerateKeyPair()
	token, _ := IssueToken("t", "a", "viewer", time.Hour, priv)
	parts := strings.SplitN(token, ".", 3)
	// Replace payload with a different base64-encoded JSON
	parts[1] = base64url(mustJSON(AppClaims{TenantID: "evil", AppID: "evil", Role: "admin"}))
	tampered := strings.Join(parts, ".")
	if _, err := Verify(tampered, &priv.PublicKey); err == nil {
		t.Error("Verify must fail for tampered payload")
	}
}

func TestSignNilKey(t *testing.T) {
	if _, err := Sign(AppClaims{}, nil); err == nil {
		t.Error("Sign must fail with nil private key")
	}
}

func TestVerifyNilKey(t *testing.T) {
	priv, _ := GenerateKeyPair()
	token, _ := IssueToken("t", "a", "viewer", time.Hour, priv)
	if _, err := Verify(token, (*rsa.PublicKey)(nil)); err == nil {
		t.Error("Verify must fail with nil public key")
	}
}

func TestIsExpired(t *testing.T) {
	past := AppClaims{Expires: time.Now().Add(-time.Second).Unix()}
	if !past.IsExpired(time.Now()) {
		t.Error("IsExpired must return true for past expiry")
	}
	future := AppClaims{Expires: time.Now().Add(time.Hour).Unix()}
	if future.IsExpired(time.Now()) {
		t.Error("IsExpired must return false for future expiry")
	}
}
