package plugins

import "strings"

// PermissionsChecker enforces the runtime permission allowlists declared in a
// Permissions value.  All methods are default-deny: they return false unless
// every relevant gate is open.
type PermissionsChecker struct {
	p Permissions
}

// NewPermissionsChecker wraps a Permissions value for runtime enforcement.
func NewPermissionsChecker(p Permissions) PermissionsChecker {
	return PermissionsChecker{p: p}
}

// AllowsHostFunction reports whether the given host function was explicitly
// declared in permissions.host_functions.
func (c PermissionsChecker) AllowsHostFunction(fn HostFunctionName) bool {
	return containsHostFn(c.p.HostFunctions, fn)
}

// AllowsNetworkHost extracts the hostname from url and checks it against
// permissions.network.  Returns false if:
//   - the "fetch" host function is not granted, or
//   - network.allowed is false, or
//   - the hostname does not match any entry in network.allowed_hosts.
//
// Wildcard patterns of the form "*.example.com" match any subdomain of
// example.com but not example.com itself.
func (c PermissionsChecker) AllowsNetworkHost(url string) bool {
	if !c.AllowsHostFunction(HostFnFetch) {
		return false
	}
	if !c.p.Network.Allowed {
		return false
	}
	host := hostnameOf(url)
	return hostAllowed(host, c.p.Network.AllowedHosts)
}

// AllowsFilePath reports whether path is permitted by the filesystem
// allowlist.  Returns false if:
//   - the "read_file" host function is not granted, or
//   - filesystem.allowed is false, or
//   - the (normalised) path contains a traversal sequence ("../"), or
//   - the path does not start with any entry in filesystem.allowed_paths.
func (c PermissionsChecker) AllowsFilePath(path string) bool {
	if !c.AllowsHostFunction(HostFnReadFile) {
		return false
	}
	if !c.p.Filesystem.Allowed {
		return false
	}
	return pathAllowed(path, c.p.Filesystem.AllowedPaths)
}

// AllowsEnvKey reports whether key may be read from the environment.
// Returns false if:
//   - the "get_env" host function is not granted, or
//   - env.allowed is false, or
//   - key is not in env.allowed_keys.
func (c PermissionsChecker) AllowsEnvKey(key string) bool {
	if !c.AllowsHostFunction(HostFnGetEnv) {
		return false
	}
	if !c.p.Env.Allowed {
		return false
	}
	for _, k := range c.p.Env.AllowedKeys {
		if k == key {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------
// Internal helpers (ported from TypeScript permissions.ts)
// -----------------------------------------------------------------------

// hostnameOf extracts the hostname (lowercased) from a URL string without
// using net/url so it stays dependency-light and matches the TS behaviour
// exactly: scheme stripped, userinfo stripped, port stripped.
func hostnameOf(url string) string {
	// Strip scheme "scheme://"
	s := url
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	// Take authority (everything before the first '/')
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	// Strip userinfo "user:pass@"
	if idx := strings.LastIndex(s, "@"); idx >= 0 {
		s = s[idx+1:]
	}
	// Strip port ":443"
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		s = s[:idx]
	}
	return strings.ToLower(s)
}

// hostAllowed checks host against an allowlist that may contain wildcard
// patterns like "*.example.com".
func hostAllowed(host string, allowed []string) bool {
	for _, pattern := range allowed {
		normalized := strings.ToLower(pattern)
		if strings.HasPrefix(normalized, "*.") {
			suffix := normalized[1:] // ".example.com"
			base := normalized[2:]   // "example.com"
			if host == base || strings.HasSuffix(host, suffix) {
				return true
			}
		} else if host == normalized {
			return true
		}
	}
	return false
}

// pathAllowed checks path against an allowlist of root prefixes.  Traversal
// sequences ("../") are unconditionally denied before the prefix check.
func pathAllowed(path string, allowed []string) bool {
	normalized := strings.ReplaceAll(path, `\`, "/")
	if strings.Contains(normalized, "../") {
		return false
	}
	for _, root := range allowed {
		normalizedRoot := strings.TrimRight(strings.ReplaceAll(root, `\`, "/"), "/")
		if normalized == normalizedRoot || strings.HasPrefix(normalized, normalizedRoot+"/") {
			return true
		}
	}
	return false
}
