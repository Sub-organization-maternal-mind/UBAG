// Package crypto provides password hashing (argon2id) and symmetric envelope
// encryption (AES-256-GCM) for UBAG gateway secrets management (§11).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Time    = 1
	argon2Memory  = 64 * 1024 // 64 MiB
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16

	aesKeyLen   = 32 // AES-256
	gcmNonceLen = 12
)

// ErrInvalidHash is returned when a hash string cannot be parsed.
var ErrInvalidHash = errors.New("crypto: invalid hash format")

// ErrDecryptFailed is returned when GCM open fails (wrong key or tampered ciphertext).
var ErrDecryptFailed = errors.New("crypto: decryption failed")

// HashPassword derives an argon2id hash of password and returns a
// self-describing string: "$argon2id$v=19$t=1,m=65536,p=4$<salt>$<hash>"
// encoded as standard base64 (no padding stripped).
func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("crypto: generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return encodeArgon2Hash(salt, hash), nil
}

// VerifyPassword reports whether password matches the argon2id hash produced
// by HashPassword. Returns false for any parse or format error.
func VerifyPassword(hash, password string) bool {
	salt, expected, err := decodeArgon2Hash(hash)
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// SealWithKEK encrypts plaintext under kek using AES-256-GCM. The returned
// ciphertext has the format: nonce (12 bytes) || GCM ciphertext+tag.
func SealWithKEK(plaintext, kek []byte) ([]byte, error) {
	if len(kek) != aesKeyLen {
		return nil, fmt.Errorf("crypto: kek must be %d bytes, got %d", aesKeyLen, len(kek))
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	nonce := make([]byte, gcmNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return sealed, nil
}

// OpenWithKEK decrypts ciphertext (nonce || GCM ciphertext+tag) under kek.
func OpenWithKEK(ciphertext, kek []byte) ([]byte, error) {
	if len(kek) != aesKeyLen {
		return nil, fmt.Errorf("crypto: kek must be %d bytes, got %d", aesKeyLen, len(kek))
	}
	if len(ciphertext) < gcmNonceLen {
		return nil, ErrDecryptFailed
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm: %w", err)
	}
	nonce := ciphertext[:gcmNonceLen]
	data := ciphertext[gcmNonceLen:]
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return plain, nil
}

// LoadKEK reads the master key-encryption key from UBAG_MASTER_KEK_HEX (a
// 64-character hex string encoding 32 bytes). Returns an error when the
// variable is absent or malformed.
func LoadKEK() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv("UBAG_MASTER_KEK_HEX"))
	if raw == "" {
		return nil, fmt.Errorf("crypto: UBAG_MASTER_KEK_HEX is not set")
	}
	kek, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("crypto: UBAG_MASTER_KEK_HEX is not valid hex: %w", err)
	}
	if len(kek) != aesKeyLen {
		return nil, fmt.Errorf("crypto: UBAG_MASTER_KEK_HEX must encode exactly %d bytes, got %d", aesKeyLen, len(kek))
	}
	return kek, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// argon2id hash encoding
// ─────────────────────────────────────────────────────────────────────────────

func encodeArgon2Hash(salt, hash []byte) string {
	return fmt.Sprintf("$argon2id$v=19$t=%d,m=%d,p=%d$%s$%s",
		argon2Time,
		argon2Memory,
		argon2Threads,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(hash),
	)
}

func decodeArgon2Hash(encoded string) (salt, hash []byte, err error) {
	parts := strings.Split(encoded, "$")
	// "$argon2id$v=19$t=...$<salt>$<hash>" → ["", "argon2id", "v=19", "t=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, ErrInvalidHash
	}
	salt, err = base64.StdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: salt: %v", ErrInvalidHash, err)
	}
	hash, err = base64.StdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: hash: %v", ErrInvalidHash, err)
	}
	return salt, hash, nil
}
