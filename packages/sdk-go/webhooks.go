package ubag

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"
)

// VerifyWebhookSignature checks an HMAC-SHA256 signature over
// `${timestamp}.${body}` within toleranceSeconds. Constant-time comparison.
func VerifyWebhookSignature(payload []byte, signature, secret, timestamp string, toleranceSeconds int64) bool {
	tsInt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	age := math.Abs(float64(time.Now().Unix() - tsInt))
	if age > float64(toleranceSeconds) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%s.%s", timestamp, string(payload))))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
