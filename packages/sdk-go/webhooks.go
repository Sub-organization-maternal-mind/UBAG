package ubag

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const webhookSignatureVersion = "v1"

// VerifyWebhookSignature checks the gateway's webhook signature: HMAC-SHA256
// over `${timestamp}.${nonce}.${body}`, base64url-encoded with a `v1=` prefix
// (gateway internal/webhooks signing.go), within toleranceSeconds.
// Constant-time comparison.
func VerifyWebhookSignature(payload []byte, signature, secret, timestamp, nonce string, toleranceSeconds int64) bool {
	tsInt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	age := math.Abs(float64(time.Now().Unix() - tsInt))
	if age > float64(toleranceSeconds) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%s.%s.%s", timestamp, nonce, string(payload))))
	expected := webhookSignatureVersion + "=" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature)) && strings.HasPrefix(signature, webhookSignatureVersion+"=")
}
