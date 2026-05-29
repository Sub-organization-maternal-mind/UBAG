package ratelimit

import (
	"sort"
	"strings"
	"time"
)

// PolicyResolver maps an action/route string (for example "job:create") to the
// Policy that governs it. Unknown actions fall back to the configured default.
// It is safe for concurrent reads; build it once at startup and treat it as
// immutable thereafter.
type PolicyResolver struct {
	defaultPolicy Policy
	overrides     map[string]Policy
}

// NewPolicyResolver builds a resolver from a default policy and a map of
// per-action overrides. The overrides map is copied so later mutation of the
// caller's map does not affect the resolver. A nil overrides map is allowed.
func NewPolicyResolver(defaultPolicy Policy, overrides map[string]Policy) *PolicyResolver {
	copied := make(map[string]Policy, len(overrides))
	for action, policy := range overrides {
		copied[normalizeAction(action)] = policy
	}
	return &PolicyResolver{defaultPolicy: defaultPolicy, overrides: copied}
}

// DefaultPolicyResolver returns a resolver pre-seeded with sensible gateway
// defaults. job:create is the most expensive action and is limited tightly;
// reads are allowed more generously. Callers may override any of these from
// configuration via NewPolicyResolver.
func DefaultPolicyResolver() *PolicyResolver {
	return NewPolicyResolver(
		Policy{Limit: 600, Window: time.Minute},
		map[string]Policy{
			"job:create": {Limit: 120, Window: time.Minute, Burst: 30},
			"job:read":   {Limit: 600, Window: time.Minute},
			"job:list":   {Limit: 300, Window: time.Minute},
			"job:cancel": {Limit: 120, Window: time.Minute},
		},
	)
}

// Resolve returns the policy for action, falling back to the default policy when
// no override is registered.
func (r *PolicyResolver) Resolve(action string) Policy {
	if r == nil {
		return Policy{}
	}
	if policy, ok := r.overrides[normalizeAction(action)]; ok {
		return policy
	}
	return r.defaultPolicy
}

// Default returns the fallback policy applied to unknown actions.
func (r *PolicyResolver) Default() Policy {
	if r == nil {
		return Policy{}
	}
	return r.defaultPolicy
}

// Policies returns a copy of every explicitly configured action policy, sorted
// by action name. The default policy is reported under the "*" key. This backs
// the GET /v1/rate-limits introspection endpoint.
func (r *PolicyResolver) Policies() map[string]Policy {
	out := make(map[string]Policy, len(r.overrides)+1)
	out["*"] = r.defaultPolicy
	for action, policy := range r.overrides {
		out[action] = policy
	}
	return out
}

// Actions returns the sorted list of explicitly configured action names.
func (r *PolicyResolver) Actions() []string {
	actions := make([]string, 0, len(r.overrides))
	for action := range r.overrides {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	return actions
}

func normalizeAction(action string) string {
	return strings.ToLower(strings.TrimSpace(action))
}
