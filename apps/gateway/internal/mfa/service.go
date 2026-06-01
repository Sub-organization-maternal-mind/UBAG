package mfa

import (
	"context"
	"time"
)

// Service is a thin wrapper around the MFA store that injects a clock for
// testability.
type Service struct {
	Store Store
	Clock func() time.Time // injectable for tests; defaults to time.Now
}

// now returns the current time via the injected Clock or time.Now.
func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

// Enroll creates a new MFA enrollment for the given request.
func (s *Service) Enroll(ctx context.Context, req EnrollRequest) (EnrollResult, error) {
	return Enroll(ctx, s.Store, req)
}

// Verify checks a TOTP or recovery code for the given user at the current time.
func (s *Service) Verify(ctx context.Context, tenantID, userID, code string) (bool, error) {
	return VerifyCode(ctx, s.Store, tenantID, userID, code, s.now())
}
