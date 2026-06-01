package jitadmin

import (
	"context"
	"time"
)

// rolePriority maps role names to numeric priority values.
// Higher numeric value = higher privilege level.
var rolePriority = map[string]int{
	"viewer":     0,
	"service":    1,
	"developer":  2,
	"operator":   3,
	"admin":      4,
	"superadmin": 5,
}

// ElevatedRole returns the highest role available to actor+tenantID given
// active grants. If no active grant grants a higher role than currentRole,
// currentRole is returned unchanged.
func ElevatedRole(ctx context.Context, store Store, actor, tenantID, currentRole string, now time.Time) string {
	grants, err := store.ActiveGrants(ctx, actor, tenantID, now)
	if err != nil || len(grants) == 0 {
		return currentRole
	}

	best := currentRole
	bestPriority := rolePriority[currentRole]

	for _, g := range grants {
		if p, ok := rolePriority[g.Role]; ok && p > bestPriority {
			best = g.Role
			bestPriority = p
		}
	}
	return best
}
