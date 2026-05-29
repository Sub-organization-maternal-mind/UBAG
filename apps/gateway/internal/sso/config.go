// Package sso provides standalone SSO (OIDC + SAML) verification primitives and
// a configuration store for the UBAG gateway. It is implemented using only the
// Go standard library (plus database/sql) and deliberately carries no external
// dependencies: JWT (RS256) verification is implemented directly on top of
// crypto/rsa, and SAML assertion verification is implemented on top of
// encoding/xml + crypto.
//
// Security invariants (enforced by construction):
//
//   - The package and its config store NEVER persist plaintext client secrets,
//     private keys, or user passwords.
//   - The config store persists only public / non-secret configuration: issuer,
//     authorization / token / JWKS URLs, client_id, allowed audiences, the IdP's
//     PUBLIC signing key/certificate (PEM), the attribute -> role/tenant/app
//     mapping, and a *reference id* for any client secret. The actual client
//     secret lives in an external secret store and is out of scope here.
//   - All timestamps are handled in UTC.
package sso

import "time"

// AttributeMapping declares how IdP attributes/claims are mapped onto UBAG
// tenant/app/role/subject/email values. It contains no secret material.
type AttributeMapping struct {
	// TenantAttribute is the attribute/claim key holding the UBAG tenant id.
	TenantAttribute string
	// AppAttribute is the attribute/claim key holding the UBAG app id.
	AppAttribute string
	// RoleAttribute is the attribute/claim key holding the IdP role value.
	RoleAttribute string
	// SubjectAttribute is the attribute/claim key holding the stable subject.
	// When empty, OIDC defaults to "sub" and SAML defaults to the NameID.
	SubjectAttribute string
	// EmailAttribute is the attribute/claim key holding the user email.
	EmailAttribute string
	// RoleValues maps raw IdP role values onto UBAG roles. When a raw value is
	// not present in the map the DefaultRole (or "viewer") is used.
	RoleValues map[string]string
	// DefaultRole is the UBAG role assigned when no role can be resolved.
	// When empty it defaults to "viewer".
	DefaultRole string
	// StaticTenantID is a fixed tenant id used when TenantAttribute resolves to
	// nothing. Useful for single-tenant IdP integrations.
	StaticTenantID string
	// StaticAppID is a fixed app id used when AppAttribute resolves to nothing.
	StaticAppID string
}

// OIDCConfig holds the public, non-secret configuration required to verify an
// OIDC ID token. It never contains a plaintext client secret; only a reference
// id (ClientSecretRef) that points at an external secret store.
type OIDCConfig struct {
	// Issuer is the expected `iss` claim value.
	Issuer string
	// AuthorizationURL / TokenURL / JWKSURL are informational endpoints used by
	// the callback handler. They are persisted but not required for offline
	// verification when public keys are configured directly.
	AuthorizationURL string
	TokenURL         string
	JWKSURL          string
	// ClientID is the OAuth2/OIDC client identifier (public).
	ClientID string
	// ClientSecretRef is a reference id (NOT the secret) for the client secret
	// stored in an external secret store.
	ClientSecretRef string
	// JWKSPublicKeysPEM is a list of PEM-encoded RSA public keys or X.509
	// certificates used to verify the ID token signature.
	JWKSPublicKeysPEM []string
	// JWKSJSON is an optional JWKS JSON blob (RFC 7517) of RSA public keys.
	JWKSJSON []byte
	// AllowedAudiences is the set of acceptable `aud` values; the token's
	// audience must intersect this set.
	AllowedAudiences []string
	// AttributeMapping maps verified claims onto UBAG principal fields.
	AttributeMapping AttributeMapping
}

// SAMLConfig holds the public, non-secret configuration required to verify a
// SAML assertion. It contains only the IdP's PUBLIC certificate.
type SAMLConfig struct {
	// EntityID is this service provider's entity id (informational).
	EntityID string
	// IdPSSOURL is the IdP single sign-on URL (informational).
	IdPSSOURL string
	// IdPCertPEM is the IdP's PUBLIC signing certificate in PEM form.
	IdPCertPEM string
	// AttributeMapping maps verified attributes onto UBAG principal fields.
	AttributeMapping AttributeMapping
}

// Claims is the verified set of OIDC ID token claims.
type Claims struct {
	Subject   string
	Email     string
	Groups    []string
	Issuer    string
	Audience  []string
	IssuedAt  time.Time
	Expiry    time.Time
	NotBefore time.Time
	// Raw is the full decoded claim set.
	Raw map[string]any
}

// Assertion is the verified set of SAML assertion data.
type Assertion struct {
	Issuer       string
	Subject      string
	NotBefore    time.Time
	NotOnOrAfter time.Time
	Attributes   map[string][]string
}

// Principal is the internal UBAG identity derived from a verified token or
// assertion. It authenticates a human operator and is distinct from the
// app-secret machine identity, but still scopes the operator to a
// tenant/app/role.
type Principal struct {
	TenantID string
	AppID    string
	Role     string
	Subject  string
	Email    string
}

// Attributes flattens the verified OIDC claims into the generic attribute map
// shape consumed by MapPrincipal. Scalar claims become single-element slices;
// array claims become multi-element slices.
func (c Claims) Attributes() map[string][]string {
	out := map[string][]string{}
	for key, value := range c.Raw {
		out[key] = valueToStrings(value)
	}
	return out
}
