package ubag

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func sign(secret, ts, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%s.%s", ts, body)))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookValid(t *testing.T) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	body := `{"event":"job.completed"}`
	sig := sign("whsec", ts, body)
	if !VerifyWebhookSignature([]byte(body), sig, "whsec", ts, 300) {
		t.Fatal("expected valid signature to verify")
	}
}

func TestVerifyWebhookBadSig(t *testing.T) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	if VerifyWebhookSignature([]byte("x"), "deadbeef", "whsec", ts, 300) {
		t.Fatal("expected bad signature to fail")
	}
}

func TestVerifyWebhookExpired(t *testing.T) {
	ts := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())
	body := "x"
	sig := sign("whsec", ts, body)
	if VerifyWebhookSignature([]byte(body), sig, "whsec", ts, 300) {
		t.Fatal("expected expired timestamp to fail")
	}
}
