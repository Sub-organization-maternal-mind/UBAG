package mfa

import (
	"crypto/rand"
	"fmt"
	"math/big"

	gocrypto "github.com/ubag/ubag/apps/gateway/internal/crypto"
)

const (
	recoveryCodeCount  = 8
	recoveryCodeLength = 10 // alphanumeric chars

	recoveryAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// GenerateRecoveryCodes returns recoveryCodeCount random recovery codes and
// their argon2id hashes (via crypto.HashPassword). Codes are one-use passwords;
// only their hashes are stored.
func GenerateRecoveryCodes() (codes []string, hashes []string, err error) {
	codes = make([]string, 0, recoveryCodeCount)
	hashes = make([]string, 0, recoveryCodeCount)

	for i := 0; i < recoveryCodeCount; i++ {
		code, err := randomAlphanumeric(recoveryCodeLength)
		if err != nil {
			return nil, nil, fmt.Errorf("mfa: generate recovery code: %w", err)
		}
		hash, err := gocrypto.HashPassword(code)
		if err != nil {
			return nil, nil, fmt.Errorf("mfa: hash recovery code: %w", err)
		}
		codes = append(codes, code)
		hashes = append(hashes, hash)
	}
	return codes, hashes, nil
}

// randomAlphanumeric generates a cryptographically random alphanumeric string
// of the given length.
func randomAlphanumeric(length int) (string, error) {
	b := make([]byte, length)
	alphabetLen := big.NewInt(int64(len(recoveryAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", err
		}
		b[i] = recoveryAlphabet[n.Int64()]
	}
	return string(b), nil
}
