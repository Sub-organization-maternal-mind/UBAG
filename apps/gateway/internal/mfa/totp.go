// Package mfa implements RFC 6238 TOTP-based multi-factor authentication for
// the UBAG gateway (§MFA Task 2.1).
package mfa

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 §5.1 mandates HMAC-SHA1 for TOTP compatibility
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// TOTP_Period is the TOTP time step in seconds (30 s, RFC 6238 §4).
const TOTP_Period = 30

// GenerateSecret returns a random 20-byte base32-encoded TOTP secret suitable
// for storage and QR-code enrollment.
func GenerateSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mfa: generate secret: %w", err)
	}
	return base32.StdEncoding.EncodeToString(raw), nil
}

// OTPAuthURI returns an otpauth:// URI for QR-code enrollment.
// Format: otpauth://totp/<issuer>:<account>?secret=<secret>&issuer=<issuer>&algorithm=SHA1&digits=6&period=30
func OTPAuthURI(issuer, account, secret string) string {
	return fmt.Sprintf(
		"otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30",
		issuer, account, secret, issuer,
	)
}

// TOTP returns the 6-digit TOTP code for secret at time t.
// Implements RFC 6238 with HMAC-SHA1 as mandated by §5.1.
// secret must be a base32-encoded string (as produced by GenerateSecret).
func TOTP(secret string, t time.Time) (string, error) {
	counter := uint64(math.Floor(float64(t.Unix()) / TOTP_Period))
	return hotp(secret, counter)
}

// Verify checks code against the TOTP window [t-1step, t, t+1step] (±1 step skew).
// Returns true if any window matches. Uses constant-time comparison.
func Verify(secret, code string, t time.Time) (bool, error) {
	counter := uint64(math.Floor(float64(t.Unix()) / TOTP_Period))
	for _, c := range []uint64{counter - 1, counter, counter + 1} {
		candidate, err := hotp(secret, c)
		if err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true, nil
		}
	}
	return false, nil
}

// hotp computes an HOTP value (RFC 4226) for the given base32-encoded secret
// and counter. The key is decoded from base32 before HMAC.
func hotp(secret string, counter uint64) (string, error) {
	key, err := base32.StdEncoding.DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("mfa: decode secret: %w", err)
	}

	// Encode counter as big-endian 8-byte value.
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, counter)

	// HMAC-SHA1 (RFC 4226 §5.3, RFC 6238 §5.1).
	mac := hmac.New(sha1.New, key) //nolint:gosec
	mac.Write(msg)
	h := mac.Sum(nil)

	// Dynamic truncation (RFC 4226 §5.3).
	offset := h[len(h)-1] & 0x0f
	binCode := (uint32(h[offset])&0x7f)<<24 |
		uint32(h[offset+1])<<16 |
		uint32(h[offset+2])<<8 |
		uint32(h[offset+3])

	otp := binCode % 1_000_000
	return fmt.Sprintf("%06d", otp), nil
}

// hotpRaw computes HOTP using a raw key (byte slice), NOT base32-encoded.
// Used only for RFC 6238 test vectors which use raw ASCII keys.
func hotpRaw(key []byte, counter uint64) (string, error) {
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, counter)

	mac := hmac.New(sha1.New, key) //nolint:gosec
	mac.Write(msg)
	h := mac.Sum(nil)

	offset := h[len(h)-1] & 0x0f
	binCode := (uint32(h[offset])&0x7f)<<24 |
		uint32(h[offset+1])<<16 |
		uint32(h[offset+2])<<8 |
		uint32(h[offset+3])

	otp := binCode % 1_000_000
	return fmt.Sprintf("%06d", otp), nil
}
