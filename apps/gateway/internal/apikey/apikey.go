// Package apikey implements the UBAG API key format:
//
//	ubag_sk_<env>_<base58(32 random bytes || 4-byte CRC32)>
//
// This matches the GitHub secret-scanning partner format (§11).
package apikey

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"regexp"
	"strings"
)

const (
	rawKeyLen   = 32 // random bytes
	checksumLen = 4  // CRC32C bytes appended before base58 encoding
	payloadLen  = rawKeyLen + checksumLen
	prefix      = "ubag_sk_"
)

// envPattern restricts the env segment to lowercase letters and digits.
var envPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)

// Key is a parsed and validated API key.
type Key struct {
	// Env is the environment segment embedded in the key (e.g. "prod", "dev").
	Env string
	// Raw is the 32-byte random secret.
	Raw []byte
	// Formatted is the complete key string as presented to the caller.
	Formatted string
}

// Generate creates a new random API key for the given environment.
func Generate(env string) (Key, error) {
	if !envPattern.MatchString(env) {
		return Key{}, fmt.Errorf("apikey: env %q must match ^[a-z][a-z0-9]{0,31}$", env)
	}
	raw := make([]byte, rawKeyLen)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return Key{}, fmt.Errorf("apikey: generate random bytes: %w", err)
	}
	payload := appendChecksum(raw)
	formatted := prefix + env + "_" + base58Encode(payload)
	return Key{Env: env, Raw: raw, Formatted: formatted}, nil
}

// Validate parses and verifies raw, returning the decoded Key or an error.
// It accepts strings with or without the "ubag_sk_" prefix (the prefix is
// required by the spec but callers may strip it before storage).
func Validate(raw string) (Key, error) {
	rest := raw
	if !strings.HasPrefix(rest, prefix) {
		return Key{}, ErrInvalidFormat
	}
	rest = strings.TrimPrefix(rest, prefix)

	// rest = "<env>_<base58>"
	idx := strings.IndexByte(rest, '_')
	if idx <= 0 {
		return Key{}, ErrInvalidFormat
	}
	env := rest[:idx]
	encoded := rest[idx+1:]

	if !envPattern.MatchString(env) {
		return Key{}, ErrInvalidFormat
	}

	payload, err := base58Decode(encoded)
	if err != nil {
		return Key{}, fmt.Errorf("%w: base58: %v", ErrInvalidFormat, err)
	}
	if len(payload) != payloadLen {
		return Key{}, ErrInvalidFormat
	}
	keyBytes := payload[:rawKeyLen]
	storedSum := binary.BigEndian.Uint32(payload[rawKeyLen:])
	if computeChecksum(keyBytes) != storedSum {
		return Key{}, ErrChecksumMismatch
	}

	return Key{Env: env, Raw: keyBytes, Formatted: raw}, nil
}

// Errors returned by Validate.
var (
	ErrInvalidFormat    = errors.New("apikey: invalid key format")
	ErrChecksumMismatch = errors.New("apikey: checksum mismatch")
)

// ─────────────────────────────────────────────────────────────────────────────
// Checksum helpers
// ─────────────────────────────────────────────────────────────────────────────

func appendChecksum(raw []byte) []byte {
	sum := computeChecksum(raw)
	payload := make([]byte, len(raw)+checksumLen)
	copy(payload, raw)
	binary.BigEndian.PutUint32(payload[len(raw):], sum)
	return payload
}

func computeChecksum(data []byte) uint32 {
	return crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))
}

// ─────────────────────────────────────────────────────────────────────────────
// Base58 encoding (Bitcoin alphabet)
// ─────────────────────────────────────────────────────────────────────────────

const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var decodeMap [256]int

func init() {
	for i := range decodeMap {
		decodeMap[i] = -1
	}
	for i, c := range alphabet {
		decodeMap[c] = i
	}
}

func base58Encode(input []byte) string {
	// Count leading zeros.
	leadingZeros := 0
	for _, b := range input {
		if b != 0 {
			break
		}
		leadingZeros++
	}

	// Convert to base58 via big-integer arithmetic. Allocate 2× input length to
	// guarantee the buffer is never exhausted (max base58 expansion ≈ 1.37×).
	size := len(input)*2 + 1
	digits := make([]byte, size)
	digitsLen := 1

	for _, b := range input {
		carry := int(b)
		for i := 0; i < digitsLen; i++ {
			carry += int(digits[i]) << 8
			digits[i] = byte(carry % 58)
			carry /= 58
		}
		for carry > 0 {
			digits[digitsLen] = byte(carry % 58)
			digitsLen++
			carry /= 58
		}
	}

	out := make([]byte, leadingZeros+digitsLen)
	for i := 0; i < leadingZeros; i++ {
		out[i] = alphabet[0]
	}
	for i := 0; i < digitsLen; i++ {
		out[leadingZeros+i] = alphabet[digits[digitsLen-1-i]]
	}
	return string(out)
}

func base58Decode(s string) ([]byte, error) {
	leadingZeros := 0
	for _, c := range s {
		if c != rune(alphabet[0]) {
			break
		}
		leadingZeros++
	}

	size := len(s)*733/1000 + 1 // ceil(log(58)/log(256)) ≈ 0.732
	digits := make([]byte, size)
	digitsLen := 1

	for _, c := range s {
		v := decodeMap[c]
		if v < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", c)
		}
		carry := v
		for i := 0; i < digitsLen; i++ {
			carry += int(digits[i]) * 58
			digits[i] = byte(carry & 0xFF)
			carry >>= 8
		}
		for carry > 0 {
			digits[digitsLen] = byte(carry & 0xFF)
			digitsLen++
			carry >>= 8
		}
	}

	// Skip trailing zeros in digits (they correspond to leading '1's in base58).
	for digitsLen > 1 && digits[digitsLen-1] == 0 {
		digitsLen--
	}

	out := make([]byte, leadingZeros+digitsLen)
	for i := 0; i < leadingZeros; i++ {
		out[i] = 0
	}
	for i := 0; i < digitsLen; i++ {
		out[leadingZeros+i] = digits[digitsLen-1-i]
	}
	return out, nil
}
