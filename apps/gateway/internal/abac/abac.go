// Package abac implements Attribute-Based Access Control using CEL predicates
// (§11 policy bundles). Rules evaluate against a request context after the
// RBAC role check passes; a deny from any rule rejects the request.
package abac

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/google/cel-go/cel"
)

// Rule is a single ABAC policy rule. Condition is a CEL expression that must
// evaluate to bool. When it evaluates to false the action is denied.
type Rule struct {
	Name      string `json:"name"`
	Condition string `json:"condition"`
}

// PolicyBundle is a loadable collection of rules. All rules must pass (logical
// AND) for a request to be allowed.
type PolicyBundle struct {
	Rules []Rule `json:"rules"`
}

// Principal carries the authenticated caller's attributes, exposed to CEL as
// the `principal` variable.
type Principal struct {
	TenantID string
	AppID    string
	Role     string
	Subject  string
}

// Enforcer evaluates ABAC rules against a request context.
type Enforcer struct {
	env    *cel.Env
	bundle PolicyBundle
}

// DefaultEnforcer returns a permissive enforcer with no rules.
// All actions are allowed (RBAC remains the only gate).
func DefaultEnforcer() *Enforcer {
	env, err := newCELEnv()
	if err != nil {
		panic("abac: failed to init CEL env: " + err.Error())
	}
	return &Enforcer{env: env}
}

// NewEnforcer returns an enforcer that evaluates bundle's rules.
func NewEnforcer(bundle PolicyBundle) (*Enforcer, error) {
	env, err := newCELEnv()
	if err != nil {
		return nil, fmt.Errorf("abac: init CEL env: %w", err)
	}
	// Pre-compile all rules to catch syntax errors eagerly.
	for _, rule := range bundle.Rules {
		ast, issues := env.Compile(rule.Condition)
		if issues != nil && issues.Err() != nil {
			return nil, fmt.Errorf("abac: rule %q compile error: %w", rule.Name, issues.Err())
		}
		prog, err := env.Program(ast)
		if err != nil {
			return nil, fmt.Errorf("abac: rule %q program error: %w", rule.Name, err)
		}
		_ = prog // programs are created lazily below; this just validates
	}
	return &Enforcer{env: env, bundle: bundle}, nil
}

// Allow evaluates all bundle rules against principal+resource+action.
// Returns true if all rules pass (or no rules exist). Returns false if any
// rule evaluates to false. Returns an error on CEL evaluation failure.
//
// A nil Enforcer is permissive: Allow always returns true, nil.
func (e *Enforcer) Allow(principal Principal, resource, action string) (bool, error) {
	if e == nil || len(e.bundle.Rules) == 0 {
		return true, nil
	}
	vars := map[string]any{
		"principal": map[string]any{
			"tenant_id": principal.TenantID,
			"app_id":    principal.AppID,
			"role":      principal.Role,
			"subject":   principal.Subject,
		},
		"resource": resource,
		"action":   action,
	}
	for _, rule := range e.bundle.Rules {
		ast, issues := e.env.Compile(rule.Condition)
		if issues != nil && issues.Err() != nil {
			return false, fmt.Errorf("abac: rule %q compile: %w", rule.Name, issues.Err())
		}
		prog, err := e.env.Program(ast)
		if err != nil {
			return false, fmt.Errorf("abac: rule %q program: %w", rule.Name, err)
		}
		out, _, err := prog.Eval(vars)
		if err != nil {
			return false, fmt.Errorf("abac: rule %q eval: %w", rule.Name, err)
		}
		result, ok := out.Value().(bool)
		if !ok {
			return false, fmt.Errorf("abac: rule %q must evaluate to bool, got %T", rule.Name, out.Value())
		}
		if !result {
			return false, nil
		}
	}
	return true, nil
}

// LoadBundleFromFile reads a JSON PolicyBundle from path.
func LoadBundleFromFile(path string) (PolicyBundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PolicyBundle{}, fmt.Errorf("abac: read bundle %q: %w", path, err)
	}
	var bundle PolicyBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return PolicyBundle{}, fmt.Errorf("abac: parse bundle %q: %w", path, err)
	}
	if len(bundle.Rules) == 0 {
		return PolicyBundle{}, errors.New("abac: bundle contains no rules")
	}
	return bundle, nil
}

func newCELEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("principal", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("resource", cel.StringType),
		cel.Variable("action", cel.StringType),
	)
}
