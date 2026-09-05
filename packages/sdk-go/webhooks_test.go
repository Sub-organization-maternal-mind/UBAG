package ubag

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

// signGatewayStyle mirrors the gateway's signing (internal/webhooks
// signing.go): HMAC-SHA256 over `${timestamp}.${nonce}.${body}`, base64url,
// "v1=" prefix.
func sign(secret, ts, nonce, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%s.%s.%s", ts, nonce, body)))
	return "v1=" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookValid(t *testing.T) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	body := `{"event":"job.completed"}`
	sig := sign("whsec", ts, "nonce_01", body)
	if !VerifyWebhookSignature([]byte(body), sig, "whsec", ts, "nonce_01", 300) {
		t.Fatal("expected valid gateway signature to verify")
	}
}

func TestVerifyWebhookWrongNonceRejected(t *testing.T) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	body := "x"
	sig := sign("whsec", ts, "nonce_01", body)
	if VerifyWebhookSignature([]byte(body), sig, "whsec", ts, "nonce_02", 300) {
		t.Fatal("expected wrong nonce (replay) to fail")
	}
}

func TestVerifyWebhookBadSig(t *testing.T) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	if VerifyWebhookSignature([]byte("x"), "v1=deadbeef", "whsec", ts, "nonce", 300) {
		t.Fatal("expected bad signature to fail")
	}
}

func TestVerifyWebhookLegacyHexRejected(t *testing.T) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	body := "x"
	// Old SDK bug: hex over `${timestamp}.${body}` — must no longer verify.
	legacyMac := hmac.New(sha256.New, []byte("whsec"))
	legacyMac.Write([]byte(ts + "." + body))
	legacy := fmt.Sprintf("%x", legacyMac.Sum(nil))
	if VerifyWebhookSignature([]byte(body), legacy, "whsec", ts, "nonce", 300) {
		t.Fatal("expected legacy hex signature format to fail")
	}
}

func TestVerifyWebhookExpired(t *testing.T) {
	ts := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())
	body := "x"
	sig := sign("whsec", ts, "nonce_01", body)
	if VerifyWebhookSignature([]byte(body), sig, "whsec", ts, "nonce_01", 300) {
		t.Fatal("expected expired timestamp to fail")
	}
}
