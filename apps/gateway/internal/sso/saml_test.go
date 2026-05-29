package sso

import (
	"context"
	"errors"
	"testing"
	"time"
)

func samlConfig(certPEM string) SAMLConfig {
	return SAMLConfig{
		EntityID:   "https://sp.ubag.example/",
		IdPSSOURL:  "https://idp.example/sso",
		IdPCertPEM: certPEM,
	}
}

func TestParseAndVerifyAssertion_Success(t *testing.T) {
	key, _, certPEM := testKeypair(t)
	now := time.Now().UTC()
	xmlBytes := buildSignedAssertion(t, key, now.Add(-time.Minute), now.Add(time.Hour), true)

	assertion, err := ParseAndVerifyAssertion(context.Background(), xmlBytes, samlConfig(certPEM), now)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if assertion.Subject != "operator@example.com" {
		t.Errorf("subject = %q", assertion.Subject)
	}
	if assertion.Issuer != "https://idp.example/" {
		t.Errorf("issuer = %q", assertion.Issuer)
	}
	if got := assertion.Attributes["tenant"]; len(got) != 1 || got[0] != "tenant-1" {
		t.Errorf("tenant attribute = %v", got)
	}
	if got := assertion.Attributes["role"]; len(got) != 1 || got[0] != "idp-admin" {
		t.Errorf("role attribute = %v", got)
	}
}

func TestParseAndVerifyAssertion_Expired(t *testing.T) {
	key, _, certPEM := testKeypair(t)
	now := time.Now().UTC()
	// NotOnOrAfter in the past (well beyond clock skew).
	xmlBytes := buildSignedAssertion(t, key, now.Add(-2*time.Hour), now.Add(-time.Hour), true)

	_, err := ParseAndVerifyAssertion(context.Background(), xmlBytes, samlConfig(certPEM), now)
	if !errors.Is(err, ErrAssertionExpired) {
		t.Fatalf("expected ErrAssertionExpired, got %v", err)
	}
}

func TestParseAndVerifyAssertion_MissingSignature(t *testing.T) {
	key, _, certPEM := testKeypair(t)
	now := time.Now().UTC()
	xmlBytes := buildSignedAssertion(t, key, now.Add(-time.Minute), now.Add(time.Hour), false)

	_, err := ParseAndVerifyAssertion(context.Background(), xmlBytes, samlConfig(certPEM), now)
	if !errors.Is(err, ErrAssertionUnsigned) {
		t.Fatalf("expected ErrAssertionUnsigned, got %v", err)
	}
}

func TestParseAndVerifyAssertion_WrongCert(t *testing.T) {
	signingKey, _, _ := testKeypair(t)
	_, _, otherCertPEM := testKeypair(t)
	now := time.Now().UTC()
	xmlBytes := buildSignedAssertion(t, signingKey, now.Add(-time.Minute), now.Add(time.Hour), true)

	_, err := ParseAndVerifyAssertion(context.Background(), xmlBytes, samlConfig(otherCertPEM), now)
	if !errors.Is(err, ErrAssertionSignature) {
		t.Fatalf("expected ErrAssertionSignature, got %v", err)
	}
}

func TestParseAndVerifyAssertion_TamperedAttribute(t *testing.T) {
	key, _, certPEM := testKeypair(t)
	now := time.Now().UTC()
	xmlBytes := buildSignedAssertion(t, key, now.Add(-time.Minute), now.Add(time.Hour), true)
	// Tamper with an attribute value after signing -> digest mismatch.
	tampered := []byte(string(xmlBytes))
	tampered = []byte(replaceFirst(string(tampered), "tenant-1", "tenant-evil"))

	_, err := ParseAndVerifyAssertion(context.Background(), tampered, samlConfig(certPEM), now)
	if !errors.Is(err, ErrAssertionSignature) {
		t.Fatalf("expected ErrAssertionSignature for tampered assertion, got %v", err)
	}
}

func replaceFirst(s, old, new string) string {
	index := indexOf(s, old)
	if index < 0 {
		return s
	}
	return s[:index] + new + s[index+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
