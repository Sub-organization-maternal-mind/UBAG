package helper

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// writeRegistry lays out a two-adapter registry; mockManifest is the mock
// adapter's manifest document.
func writeRegistry(t *testing.T, dir, mockManifest string) string {
	t.Helper()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("registry.json", `{"schema_version":"v0","adapters":[{"id":"mock","manifest":"mock/manifest.json"},{"id":"chatgpt_web","manifest":"chatgpt_web/manifest.json"}]}`)
	write("mock/manifest.json", mockManifest)
	write("chatgpt_web/manifest.json", `{"id":"chatgpt_web","timeout":1.50}`)
	return dir
}

func TestRegistryDigestIsCanonicalAndSensitive(t *testing.T) {
	digest := func(mock string) string {
		t.Helper()
		d, err := RegistryDigest(writeRegistry(t, t.TempDir(), mock))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	base := digest(`{"id":"mock","selectors":{"a":1,"b":2}}`)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(base) {
		t.Fatalf("not a sha256 hex digest: %q", base)
	}
	if got := digest("{\r\n  \"selectors\": { \"b\": 2,\r\n \"a\": 1 },\r\n  \"id\": \"mock\"\r\n}\r\n"); got != base {
		t.Error("key order, whitespace and CRLF must not change the digest")
	}
	for name, changed := range map[string]string{
		"a changed value":  `{"id":"mock","selectors":{"a":1,"b":3}}`,
		"an added key":     `{"id":"mock","selectors":{"a":1,"b":2},"x":1}`,
		"a changed number": `{"id":"mock","selectors":{"a":1.0,"b":2}}`,
	} {
		if digest(changed) == base {
			t.Errorf("%s must change the digest", name)
		}
	}

	// The registry index and the other manifests count too.
	dir := writeRegistry(t, t.TempDir(), `{"id":"mock","selectors":{"a":1,"b":2}}`)
	if err := os.WriteFile(filepath.Join(dir, "chatgpt_web", "manifest.json"), []byte(`{"id":"chatgpt_web","timeout":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, _ := RegistryDigest(dir); d == base {
		t.Error("another adapter's manifest must change the digest")
	}
	dir = writeRegistry(t, t.TempDir(), `{"id":"mock","selectors":{"a":1,"b":2}}`)
	if err := os.WriteFile(filepath.Join(dir, "registry.json"), []byte(`{"adapters":[{"id":"mock","manifest":"mock/manifest.json"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, _ := RegistryDigest(dir); d == base {
		t.Error("the adapter set must change the digest")
	}
}

func TestRegistryDigestFailsClosed(t *testing.T) {
	dir := writeRegistry(t, t.TempDir(), `{"id":"mock"}`)
	set := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct{ rel, body string }{
		"registry is not json":      {"registry.json", `not json`},
		"no adapters":               {"registry.json", `{"adapters":[]}`},
		"traversing manifest path":  {"registry.json", `{"adapters":[{"id":"a","manifest":"../../etc/passwd"}]}`},
		"absolute manifest path":    {"registry.json", `{"adapters":[{"id":"a","manifest":"/etc/passwd"}]}`},
		"duplicate adapter id":      {"registry.json", `{"adapters":[{"id":"a","manifest":"mock/manifest.json"},{"id":"a","manifest":"mock/manifest.json"}]}`},
		"adapter without an id":     {"registry.json", `{"adapters":[{"manifest":"mock/manifest.json"}]}`},
		"missing manifest file":     {"registry.json", `{"adapters":[{"id":"a","manifest":"nope/manifest.json"}]}`},
		"manifest is not json":      {"mock/manifest.json", `{`},
		"manifest with trailing":    {"mock/manifest.json", `{} {}`},
		"manifest larger than 4MiB": {"mock/manifest.json", `{"x":"` + strings.Repeat("y", maxRegistryFileBytes) + `"}`},
	} {
		dir = writeRegistry(t, t.TempDir(), `{"id":"mock"}`)
		set(tc.rel, tc.body)
		if d, err := RegistryDigest(dir); err == nil || d != "" {
			t.Errorf("%s: must be an error, got %q %v", name, d, err)
		}
	}
	if _, err := RegistryDigest(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a missing registry directory must be an error")
	}
}

// The real adapters/ directory must load: every manifest in the registry parses
// and the digest is stable, which is what the primary will compare against.
func TestRegistryDigestOfTheRepositoryAdapters(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", "adapters")
	if _, err := os.Stat(filepath.Join(dir, "registry.json")); err != nil {
		t.Skip("repository adapters/ directory not found from this working directory")
	}
	a, err := RegistryDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := RegistryDigest(dir); a != b || len(a) != 64 {
		t.Fatalf("digest not stable: %q %q", a, b)
	}
}
