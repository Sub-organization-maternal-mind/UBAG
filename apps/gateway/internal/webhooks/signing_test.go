package webhooks

import (
	"strings"
	"testing"
	"time"
)

func TestSignBodyMatchesSecurityContractFixture(t *testing.T) {
	headers, err := SignBody([]byte("secret_fixture"), []byte(`{"ok":true}`), time.Unix(1700000000, 0), "nonce_fixture_123456")
	if err != nil {
		t.Fatalf("SignBody returned error: %v", err)
	}
	if headers.Signature != "v1=gM1Gv4mZwdSTV6M_RuAHh8LzVzxDv0zSluZLf2frY5k" {
		t.Fatalf("signature = %q", headers.Signature)
	}
	if string(BuildBaseString(headers.Timestamp, headers.Nonce, []byte(`{"ok":true}`))) != `1700000000.nonce_fixture_123456.{"ok":true}` {
		t.Fatalf("base string changed")
	}
}

func TestNewNonceIsHeaderSafe(t *testing.T) {
	nonce, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce returned error: %v", err)
	}
	if len(nonce) < 16 || strings.ContainsAny(nonce, "+/=") {
		t.Fatalf("nonce is not base64url header safe: %q", nonce)
	}
}
