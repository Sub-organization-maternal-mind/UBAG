package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

const (
	SignatureVersion  = "v1"
	SignatureHeader   = "Ubag-Webhook-Signature"
	TimestampHeader   = "Ubag-Webhook-Timestamp"
	NonceHeader       = "Ubag-Webhook-Nonce"
	DeliveryIDHeader  = "Ubag-Webhook-Delivery-Id"
	WebhookIDHeader   = "Ubag-Webhook-Id"
	JobIDHeader       = "Ubag-Job-Id"
	EventIDHeader     = "Ubag-Event-Id"
	TraceIDHeader     = "Ubag-Trace-Id"
	EventHeader       = "Ubag-Webhook-Event"
	APIVersionHeader  = "Ubag-Api-Version"
	contentTypeJSON   = "application/json"
	defaultNonceBytes = 18
)

type SignatureHeaders struct {
	Signature string
	Timestamp int64
	Nonce     string
}

func BuildBaseString(timestamp int64, nonce string, body []byte) []byte {
	return []byte(strconv.FormatInt(timestamp, 10) + "." + nonce + "." + string(body))
}

func SignBody(secret []byte, body []byte, now time.Time, nonce string) (SignatureHeaders, error) {
	if len(secret) == 0 {
		return SignatureHeaders{}, fmt.Errorf("webhook secret is required")
	}
	if nonce == "" {
		generated, err := NewNonce()
		if err != nil {
			return SignatureHeaders{}, err
		}
		nonce = generated
	}
	timestamp := now.UTC().Unix()
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(BuildBaseString(timestamp, nonce, body))
	digest := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return SignatureHeaders{
		Signature: SignatureVersion + "=" + digest,
		Timestamp: timestamp,
		Nonce:     nonce,
	}, nil
}

func NewNonce() (string, error) {
	raw := make([]byte, defaultNonceBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
