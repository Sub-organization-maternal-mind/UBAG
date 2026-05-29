package sso

import (
	"errors"
	"strings"
)

var (
	// ErrTenantUnresolved indicates no tenant id could be resolved from the
	// supplied attributes or static mapping fallback.
	ErrTenantUnresolved = errors.New("sso: could not resolve tenant id from attributes")
	// ErrAppUnresolved indicates no app id could be resolved.
	ErrAppUnresolved = errors.New("sso: could not resolve app id from attributes")
)

// MapPrincipal maps verified IdP attributes/claims onto a UBAG Principal using
// mapping. The resolved role defaults to "viewer" when no role can be
// determined. The function rejects the mapping when a required tenant or app id
// cannot be resolved.
func MapPrincipal(attrs map[string][]string, mapping AttributeMapping) (Principal, error) {
	tenant := firstAttr(attrs, mapping.TenantAttribute)
	if tenant == "" {
		tenant = mapping.StaticTenantID
	}
	if tenant == "" {
		return Principal{}, ErrTenantUnresolved
	}

	app := firstAttr(attrs, mapping.AppAttribute)
	if app == "" {
		app = mapping.StaticAppID
	}
	if app == "" {
		return Principal{}, ErrAppUnresolved
	}

	defaultRole := strings.TrimSpace(mapping.DefaultRole)
	if defaultRole == "" {
		defaultRole = "viewer"
	}
	role := defaultRole
	if rawRole := firstAttr(attrs, mapping.RoleAttribute); rawRole != "" {
		if mapped, ok := mapping.RoleValues[rawRole]; ok && strings.TrimSpace(mapped) != "" {
			role = mapped
		} else {
			role = rawRole
		}
	}

	return Principal{
		TenantID: tenant,
		AppID:    app,
		Role:     role,
		Subject:  firstAttr(attrs, mapping.SubjectAttribute),
		Email:    firstAttr(attrs, mapping.EmailAttribute),
	}, nil
}

func firstAttr(attrs map[string][]string, key string) string {
	if key == "" {
		return ""
	}
	for _, value := range attrs[key] {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
