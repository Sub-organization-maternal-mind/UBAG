package plugins_test

import (
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/plugins"
)

// makeChecker creates a PermissionsChecker from inline Permissions for convenience.
func makeChecker(p plugins.Permissions) plugins.PermissionsChecker {
	return plugins.NewPermissionsChecker(p)
}

// --------------------------------------------------------------------------
// AllowsHostFunction
// --------------------------------------------------------------------------

func TestAllowsHostFunction_Granted(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnLog, plugins.HostFnFetch},
	}
	c := makeChecker(p)
	if !c.AllowsHostFunction(plugins.HostFnLog) {
		t.Error("log should be allowed")
	}
	if !c.AllowsHostFunction(plugins.HostFnFetch) {
		t.Error("fetch should be allowed")
	}
}

func TestAllowsHostFunction_Denied(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnLog},
	}
	c := makeChecker(p)
	if c.AllowsHostFunction(plugins.HostFnFetch) {
		t.Error("fetch should be denied when not in host_functions")
	}
	if c.AllowsHostFunction(plugins.HostFnReadFile) {
		t.Error("read_file should be denied when not in host_functions")
	}
}

func TestAllowsHostFunction_EmptyList(t *testing.T) {
	c := makeChecker(plugins.Permissions{})
	for _, fn := range []plugins.HostFunctionName{
		plugins.HostFnLog, plugins.HostFnClock, plugins.HostFnRandom,
		plugins.HostFnFetch, plugins.HostFnReadFile, plugins.HostFnGetEnv,
	} {
		if c.AllowsHostFunction(fn) {
			t.Errorf("%q should be denied on empty permissions", fn)
		}
	}
}

// --------------------------------------------------------------------------
// AllowsNetworkHost — hostname extraction
// --------------------------------------------------------------------------

func TestAllowsNetworkHost_HostnameExtraction(t *testing.T) {
	tests := []struct {
		url      string
		wantHost string
	}{
		{"https://api.example.com/path", "api.example.com"},
		{"http://example.com", "example.com"},
		{"https://user:pass@host.io:443/foo", "host.io"},
		{"ftp://files.net/pub/file.txt", "files.net"},
		{"https://UPPER.COM/path", "upper.com"}, // lowercased
	}
	for _, tc := range tests {
		p := plugins.Permissions{
			HostFunctions: []plugins.HostFunctionName{plugins.HostFnFetch},
			Network: plugins.NetworkPermission{
				Allowed:      true,
				AllowedHosts: []string{tc.wantHost},
			},
		}
		c := makeChecker(p)
		if !c.AllowsNetworkHost(tc.url) {
			t.Errorf("url %q: expected host %q to be allowed", tc.url, tc.wantHost)
		}
	}
}

// --------------------------------------------------------------------------
// AllowsNetworkHost — wildcard matching
// --------------------------------------------------------------------------

func TestAllowsNetworkHost_WildcardMatch(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnFetch},
		Network: plugins.NetworkPermission{
			Allowed:      true,
			AllowedHosts: []string{"*.example.com"},
		},
	}
	c := makeChecker(p)

	// sub.example.com matches *.example.com
	if !c.AllowsNetworkHost("https://sub.example.com/api") {
		t.Error("sub.example.com should match *.example.com")
	}
	// deep.sub.example.com should also match (ends with .example.com)
	if !c.AllowsNetworkHost("https://deep.sub.example.com/api") {
		t.Error("deep.sub.example.com should match *.example.com")
	}
	// example.com itself should NOT match *.example.com
	if c.AllowsNetworkHost("https://example.com/api") {
		t.Error("example.com should NOT match *.example.com")
	}
	// other.net should NOT match
	if c.AllowsNetworkHost("https://other.net/api") {
		t.Error("other.net should not match *.example.com")
	}
}

func TestAllowsNetworkHost_ExactMatch(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnFetch},
		Network: plugins.NetworkPermission{
			Allowed:      true,
			AllowedHosts: []string{"example.com"},
		},
	}
	c := makeChecker(p)

	if !c.AllowsNetworkHost("https://example.com/foo") {
		t.Error("exact match should be allowed")
	}
	if c.AllowsNetworkHost("https://sub.example.com/foo") {
		t.Error("subdomain should not match exact pattern")
	}
}

func TestAllowsNetworkHost_NetworkNotAllowed(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnFetch},
		Network: plugins.NetworkPermission{
			Allowed:      false,
			AllowedHosts: []string{"example.com"},
		},
	}
	c := makeChecker(p)
	if c.AllowsNetworkHost("https://example.com/foo") {
		t.Error("should be denied when network.allowed=false")
	}
}

func TestAllowsNetworkHost_FetchNotGranted(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnLog},
		Network: plugins.NetworkPermission{
			Allowed:      true,
			AllowedHosts: []string{"example.com"},
		},
	}
	c := makeChecker(p)
	if c.AllowsNetworkHost("https://example.com/foo") {
		t.Error("should be denied when fetch not in host_functions")
	}
}

// --------------------------------------------------------------------------
// AllowsFilePath
// --------------------------------------------------------------------------

func TestAllowsFilePath_PathPrefix(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnReadFile},
		Filesystem: plugins.FilesystemPermission{
			Allowed:      true,
			AllowedPaths: []string{"/data"},
		},
	}
	c := makeChecker(p)

	if !c.AllowsFilePath("/data/files/foo.txt") {
		t.Error("/data/files/foo.txt should be under /data")
	}
	if !c.AllowsFilePath("/data") {
		t.Error("/data itself should be allowed")
	}
	if c.AllowsFilePath("/etc/passwd") {
		t.Error("/etc/passwd should not be under /data")
	}
}

func TestAllowsFilePath_TraversalDenied(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnReadFile},
		Filesystem: plugins.FilesystemPermission{
			Allowed:      true,
			AllowedPaths: []string{"/data"},
		},
	}
	c := makeChecker(p)

	if c.AllowsFilePath("../etc/passwd") {
		t.Error("path traversal should be denied")
	}
	if c.AllowsFilePath("/data/../etc/passwd") {
		t.Error("embedded path traversal should be denied")
	}
}

func TestAllowsFilePath_BackslashNormalized(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnReadFile},
		Filesystem: plugins.FilesystemPermission{
			Allowed:      true,
			AllowedPaths: []string{"/data"},
		},
	}
	c := makeChecker(p)

	// Windows-style path should still traverse-check properly
	if c.AllowsFilePath(`\data\..\etc\passwd`) {
		t.Error("backslash traversal should be denied")
	}
}

func TestAllowsFilePath_FilesystemNotAllowed(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnReadFile},
		Filesystem: plugins.FilesystemPermission{
			Allowed:      false,
			AllowedPaths: []string{"/data"},
		},
	}
	c := makeChecker(p)
	if c.AllowsFilePath("/data/foo.txt") {
		t.Error("should be denied when filesystem.allowed=false")
	}
}

func TestAllowsFilePath_ReadFileNotGranted(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnLog},
		Filesystem: plugins.FilesystemPermission{
			Allowed:      true,
			AllowedPaths: []string{"/data"},
		},
	}
	c := makeChecker(p)
	if c.AllowsFilePath("/data/foo.txt") {
		t.Error("should be denied when read_file not in host_functions")
	}
}

// --------------------------------------------------------------------------
// AllowsEnvKey
// --------------------------------------------------------------------------

func TestAllowsEnvKey_Listed(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnGetEnv},
		Env: plugins.EnvPermission{
			Allowed:     true,
			AllowedKeys: []string{"HOME", "PATH"},
		},
	}
	c := makeChecker(p)
	if !c.AllowsEnvKey("HOME") {
		t.Error("HOME should be allowed")
	}
	if c.AllowsEnvKey("SECRET") {
		t.Error("SECRET should be denied")
	}
}

func TestAllowsEnvKey_EnvNotAllowed(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnGetEnv},
		Env: plugins.EnvPermission{
			Allowed:     false,
			AllowedKeys: []string{"HOME"},
		},
	}
	c := makeChecker(p)
	if c.AllowsEnvKey("HOME") {
		t.Error("should be denied when env.allowed=false")
	}
}

func TestAllowsEnvKey_GetEnvNotGranted(t *testing.T) {
	p := plugins.Permissions{
		HostFunctions: []plugins.HostFunctionName{plugins.HostFnLog},
		Env: plugins.EnvPermission{
			Allowed:     true,
			AllowedKeys: []string{"HOME"},
		},
	}
	c := makeChecker(p)
	if c.AllowsEnvKey("HOME") {
		t.Error("should be denied when get_env not in host_functions")
	}
}
