package mfa

import (
	"context"
	"crypto/subtle"
	"fmt"
	"math"
	"time"
)

// EnrollRequest is the input to the MFA enroll flow.
type EnrollRequest struct {
	TenantID string
	UserID   string
	Issuer   string // e.g. "UBAG"
}

// EnrollResult is returned from Enroll.
type EnrollResult struct {
	Secret        string   // base32 secret (present so user can enter manually)
	OTPAuthURI    string   // for QR code
	RecoveryCodes []string // plaintext, show once
}

// Enroll creates a new MFA enrollment, persisting hashed recovery codes and
// the plaintext TOTP secret.
func Enroll(ctx context.Context, store Store, req EnrollRequest) (EnrollResult, error) {
	secret, err := GenerateSecret()
	if err != nil {
		return EnrollResult{}, fmt.Errorf("mfa: enroll: %w", err)
	}

	codes, hashes, err := GenerateRecoveryCodes()
	if err != nil {
		return EnrollResult{}, fmt.Errorf("mfa: enroll recovery codes: %w", err)
	}

	issuer := req.Issuer
	if issuer == "" {
		issuer = "UBAG"
	}

	e := Enrollment{
		TenantID:       req.TenantID,
		UserID:         req.UserID,
		Secret:         secret,
		RecoveryHashes: hashes,
		CreatedAt:      time.Now(),
	}
	if err := store.Enroll(ctx, e); err != nil {
		return EnrollResult{}, fmt.Errorf("mfa: persist enrollment: %w", err)
	}

	return EnrollResult{
		Secret:        secret,
		OTPAuthURI:    OTPAuthURI(issuer, req.UserID, secret),
		RecoveryCodes: codes,
	}, nil
}

// VerifyCode checks a TOTP code (6 digits) or recovery code (10 alphanumeric
// chars) for the given user. It prevents replay attacks by tracking consumed
// TOTP counter steps.
func VerifyCode(ctx context.Context, store Store, tenantID, userID, code string, now time.Time) (bool, error) {
	enrollment, ok, err := store.Get(ctx, tenantID, userID)
	if err != nil {
		return false, fmt.Errorf("mfa: get enrollment: %w", err)
	}
	if !ok {
		return false, nil
	}

	// Try TOTP if the code looks like a 6-digit PIN.
	if len(code) == 6 {
		counter := uint64(math.Floor(float64(now.Unix()) / TOTP_Period))

		// Check ±1 step window.
		for _, c := range []uint64{counter - 1, counter, counter + 1} {
			candidate, err := hotp(enrollment.Secret, c)
			if err != nil {
				return false, fmt.Errorf("mfa: compute totp: %w", err)
			}
			// Use constant-time comparison to prevent timing side-channels.
			if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
				// Atomically check-and-mark: prevents TOCTOU replay attack.
				marked, err := store.MarkCounterUsed(ctx, tenantID, userID, c)
				if err != nil {
					return false, err
				}
				if !marked {
					return false, nil // already used — replay attack
				}
				return true, nil
			}
		}
		return false, nil
	}

	// Fall through to recovery code path for non-6-digit inputs.
	return store.ConsumeRecovery(ctx, tenantID, userID, code)
}
