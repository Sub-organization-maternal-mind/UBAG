// Package jitadmin provides time-boxed privilege elevation ("just-in-time
// admin") for the UBAG gateway. An operator (or any authenticated actor) may
// request a temporary elevation to a higher role; an admin or superadmin can
// approve the request. The grant expires automatically after the requested TTL.
package jitadmin

import (
	"errors"
	"time"
)

// Grant represents a single JIT elevation request and its lifecycle state.
type Grant struct {
	ID         string        `json:"id"`
	Actor      string        `json:"actor"` // subject of the requesting principal
	TenantID   string        `json:"tenant_id"`
	AppID      string        `json:"app_id"`
	Role       string        `json:"role"` // the elevated role being requested
	Reason     string        `json:"reason"`
	TTL        time.Duration `json:"ttl_seconds"` // duration requested
	ExpiresAt  time.Time     `json:"expires_at"`
	Approved   bool          `json:"approved"`
	ApprovedBy string        `json:"approved_by,omitempty"`
	ApprovedAt *time.Time    `json:"approved_at,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	Revoked    bool          `json:"revoked"`
}

// IsActive returns true if the grant is approved, unexpired, and not revoked.
func (g Grant) IsActive(now time.Time) bool {
	return g.Approved && !g.Revoked && now.Before(g.ExpiresAt)
}

// ErrGrantNotFound is returned when a grant ID does not exist.
var ErrGrantNotFound = errors.New("jitadmin: grant not found")

// ErrGrantExpired is returned when a grant has passed its TTL.
var ErrGrantExpired = errors.New("jitadmin: grant has expired")
