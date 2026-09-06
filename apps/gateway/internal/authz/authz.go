// Package authz is the single RBAC policy for the whole gateway (ADR-0004
// discipline at the authorization seam). Both transports — httpapi and
// grpcapi — consume RoleAllows; neither carries its own role table. The
// httpapi table is the source truth (it is the enforced, tested superset);
// gRPC's historical subset falls out automatically because it only ever
// authorizes the four job actions.
package authz

// RoleAllows reports whether a role may perform an action. The one table.
func RoleAllows(role string, action string) bool {
	actions, ok := roleActions[role]
	if !ok {
		return false
	}
	_, permitted := actions[action]
	return permitted
}

// Actions returns the sorted action list a role may perform (introspection
// for tests and tooling).
func Actions(role string) []string {
	allowed, ok := roleActions[role]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(allowed))
	for action := range allowed {
		out = append(out, action)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

var roleActions = map[string]map[string]struct{}{
	"viewer":    {
		"job:read": {},
	},
	"developer":          {
		"job:create":        {},
		"job:read":          {},
		"job:cancel":        {},
		"job:retry":         {},
		"artifact:write":    {},
		"artifact:delete":   {},
		"webhook:configure": {},
		"browser:read":      {},
		"concurrency:read":  {},
	},
	"operator":           {
		"job:create":        {},
		"job:read":          {},
		"job:cancel":        {},
		"job:retry":         {},
		"artifact:write":    {},
		"artifact:delete":   {},
		"device:enroll":     {},
		"device:revoke":     {},
		"webhook:configure": {},
		"webhook:replay":    {},
		"audit:read":        {},
		"alerts:read":       {},
		"alerts:manage":     {},
		"browser:read":      {},
		"concurrency:read":  {},
	},
	"admin":              {
		"job:create":        {},
		"job:read":          {},
		"job:cancel":        {},
		"job:retry":         {},
		"artifact:write":    {},
		"artifact:delete":   {},
		"device:enroll":     {},
		"device:revoke":     {},
		"secret:rotate":     {},
		"webhook:configure": {},
		"webhook:replay":    {},
		"audit:read":        {},
		"rate_limit:manage": {},
		"role:manage":       {},
		"data:export":       {},
		"alerts:read":       {},
		"alerts:manage":     {},
		"browser:read":      {},
		"concurrency:read":  {},
		"region:manage":     {},
	},
	"superadmin":         {
		"job:create":        {},
		"job:read":          {},
		"job:cancel":        {},
		"job:retry":         {},
		"artifact:write":    {},
		"artifact:delete":   {},
		"device:enroll":     {},
		"device:revoke":     {},
		"secret:rotate":     {},
		"webhook:configure": {},
		"webhook:replay":    {},
		"audit:read":        {},
		"rate_limit:manage": {},
		"role:manage":       {},
		"data:export":       {},
		"alerts:read":       {},
		"alerts:manage":     {},
		"browser:read":      {},
		"concurrency:read":  {},
		"region:manage":     {},
	},
	"service":          {
		"job:create":      {},
		"job:read":        {},
		"job:cancel":      {},
		"job:retry":       {},
		"artifact:write":  {},
		"artifact:delete": {},
		"webhook:replay":  {},
	},
}
