package plugins_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/plugins"
)

// validManifestJSON returns a JSON object that satisfies every rule.
func validManifestJSON() map[string]any {
	return map[string]any{
		"schema_version": "ubag.plugin.v0",
		"id":             "my-plugin",
		"display_name":   "My Plugin",
		"version":        "1.2.3",
		"description":    "A test plugin",
		"capabilities":   []any{"transform.prompt"},
		"entrypoint": map[string]any{
			"type":    "wasi-component",
			"module":  "plugin.wasm",
			"exports": map[string]any{"transform": "run"},
		},
		"permissions": map[string]any{
			"host_functions": []any{"log"},
			"network": map[string]any{
				"allowed":       false,
				"allowed_hosts": []any{},
			},
			"filesystem": map[string]any{
				"allowed":       false,
				"allowed_paths": []any{},
			},
			"env": map[string]any{
				"allowed":      false,
				"allowed_keys": []any{},
			},
			"max_memory_bytes": float64(65536),
			"max_execution_ms": float64(1),
		},
		"engine": map[string]any{
			"runtime": "wasi-preview2",
		},
	}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// mutate creates a shallow copy of the map, applies f, and marshals.
func mutate(t *testing.T, base map[string]any, f func(m map[string]any)) []byte {
	t.Helper()
	cp := make(map[string]any, len(base))
	for k, v := range base {
		cp[k] = v
	}
	f(cp)
	return marshal(t, cp)
}

// --------------------------------------------------------------------------
// Happy-path
// --------------------------------------------------------------------------

func TestParseManifest_Valid(t *testing.T) {
	m, err := plugins.ParseManifest(marshal(t, validManifestJSON()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.ID != "my-plugin" {
		t.Errorf("ID = %q, want %q", m.ID, "my-plugin")
	}
	if m.DisplayName != "My Plugin" {
		t.Errorf("DisplayName = %q", m.DisplayName)
	}
	if m.Version != "1.2.3" {
		t.Errorf("Version = %q", m.Version)
	}
	if m.SchemaVersion != "ubag.plugin.v0" {
		t.Errorf("SchemaVersion = %q", m.SchemaVersion)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != plugins.CapabilityTransformPrompt {
		t.Errorf("Capabilities = %v", m.Capabilities)
	}
	if m.Entrypoint.Type != plugins.EntrypointWASIComponent {
		t.Errorf("Entrypoint.Type = %q", m.Entrypoint.Type)
	}
	if m.Entrypoint.Module != "plugin.wasm" {
		t.Errorf("Entrypoint.Module = %q", m.Entrypoint.Module)
	}
	if m.Engine.Runtime != plugins.RuntimeWASIPreview2 {
		t.Errorf("Engine.Runtime = %q", m.Engine.Runtime)
	}
	if m.Permissions.MaxMemoryBytes != 65536 {
		t.Errorf("MaxMemoryBytes = %d", m.Permissions.MaxMemoryBytes)
	}
	if m.Permissions.MaxExecutionMS != 1 {
		t.Errorf("MaxExecutionMS = %d", m.Permissions.MaxExecutionMS)
	}
}

func TestParseManifest_Defaults(t *testing.T) {
	// When max_memory_bytes and max_execution_ms are omitted they get defaults.
	base := validManifestJSON()
	perms := base["permissions"].(map[string]any)
	delete(perms, "max_memory_bytes")
	delete(perms, "max_execution_ms")
	base["permissions"] = perms

	m, err := plugins.ParseManifest(marshal(t, base))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Permissions.MaxMemoryBytes != 16_777_216 {
		t.Errorf("default MaxMemoryBytes = %d, want 16777216", m.Permissions.MaxMemoryBytes)
	}
	if m.Permissions.MaxExecutionMS != 1_000 {
		t.Errorf("default MaxExecutionMS = %d, want 1000", m.Permissions.MaxExecutionMS)
	}
}

func TestParseManifest_SemverVariants(t *testing.T) {
	for _, ver := range []string{"0.0.1", "1.2.3-alpha", "1.0.0+build.42", "100.200.300-rc.1+sha"} {
		data := mutate(t, validManifestJSON(), func(m map[string]any) { m["version"] = ver })
		if _, err := plugins.ParseManifest(data); err != nil {
			t.Errorf("version %q rejected: %v", ver, err)
		}
	}
}

func TestParseManifest_AllCapabilities(t *testing.T) {
	caps := []any{
		"transform.prompt",
		"transform.response",
		"hook.job.pre",
		"hook.job.post",
	}
	data := mutate(t, validManifestJSON(), func(m map[string]any) { m["capabilities"] = caps })
	mf, err := plugins.ParseManifest(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mf.Capabilities) != 4 {
		t.Errorf("len(Capabilities) = %d, want 4", len(mf.Capabilities))
	}
}

// --------------------------------------------------------------------------
// schema_version
// --------------------------------------------------------------------------

func TestParseManifest_WrongSchemaVersion(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) { m["schema_version"] = "ubag.plugin.v99" })
	assertValidationError(t, data, "schema_version")
}

// --------------------------------------------------------------------------
// id
// --------------------------------------------------------------------------

func TestParseManifest_IDValidation(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"a", false}, // too short (must be at least 2 chars)
		{"ab", true}, // exactly 2 chars
		{"my-plugin", true},
		{"my_plugin", true},
		{"my plugin", false}, // space
		{"MyPlugin", false},  // uppercase
		{"0abc", true},       // starts with digit OK
		{"UPPER", false},
		{strings.Repeat("a", 65), false}, // 65 chars => too long (max 2+62=64)
		{strings.Repeat("a", 64), true},  // 64 chars OK
		{"-start", false},                // starts with hyphen
		{"_start", false},                // starts with underscore
	}
	for _, tc := range tests {
		data := mutate(t, validManifestJSON(), func(m map[string]any) { m["id"] = tc.id })
		_, err := plugins.ParseManifest(data)
		got := err == nil
		if got != tc.want {
			t.Errorf("id=%q: got ok=%v, want %v (err=%v)", tc.id, got, tc.want, err)
		}
	}
}

func TestParseManifest_MissingID(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) { delete(m, "id") })
	assertValidationError(t, data, "id")
}

// --------------------------------------------------------------------------
// display_name
// --------------------------------------------------------------------------

func TestParseManifest_EmptyDisplayName(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) { m["display_name"] = "" })
	assertValidationError(t, data, "display_name")
}

func TestParseManifest_MissingDisplayName(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) { delete(m, "display_name") })
	assertValidationError(t, data, "display_name")
}

// --------------------------------------------------------------------------
// version
// --------------------------------------------------------------------------

func TestParseManifest_BadVersion(t *testing.T) {
	for _, v := range []string{"1.0", "v1.0.0", "1.0.0.0", "latest"} {
		data := mutate(t, validManifestJSON(), func(m map[string]any) { m["version"] = v })
		assertValidationError(t, data, "version")
	}
}

func TestParseManifest_MissingVersion(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) { delete(m, "version") })
	assertValidationError(t, data, "version")
}

// --------------------------------------------------------------------------
// capabilities
// --------------------------------------------------------------------------

func TestParseManifest_UnknownCapability(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		m["capabilities"] = []any{"transform.prompt", "magic.capability"}
	})
	var ve *plugins.ValidationError
	_, err := plugins.ParseManifest(data)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	found := false
	for _, issue := range ve.Issues {
		if strings.Contains(issue, "magic.capability") {
			found = true
		}
	}
	if !found {
		t.Errorf("issues don't mention the bad capability name: %v", ve.Issues)
	}
}

func TestParseManifest_EmptyCapabilities(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) { m["capabilities"] = []any{} })
	assertValidationError(t, data, "capabilities")
}

func TestParseManifest_MissingCapabilities(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) { delete(m, "capabilities") })
	assertValidationError(t, data, "capabilities")
}

// --------------------------------------------------------------------------
// host_functions
// --------------------------------------------------------------------------

func TestParseManifest_UnknownHostFunction(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		perms := copyMap(m["permissions"].(map[string]any))
		perms["host_functions"] = []any{"log", "fly"}
		m["permissions"] = perms
	})
	assertValidationError(t, data, "fly")
}

// --------------------------------------------------------------------------
// network/filesystem/env cross-checks
// --------------------------------------------------------------------------

func TestParseManifest_NetworkAllowedWithoutFetch(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		perms := copyMap(m["permissions"].(map[string]any))
		perms["host_functions"] = []any{"log"}
		perms["network"] = map[string]any{
			"allowed":       true,
			"allowed_hosts": []any{"example.com"},
		}
		m["permissions"] = perms
	})
	assertValidationError(t, data, "fetch")
}

func TestParseManifest_FilesystemAllowedWithoutReadFile(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		perms := copyMap(m["permissions"].(map[string]any))
		perms["host_functions"] = []any{"log"}
		perms["filesystem"] = map[string]any{
			"allowed":       true,
			"allowed_paths": []any{"/data"},
		}
		m["permissions"] = perms
	})
	assertValidationError(t, data, "read_file")
}

func TestParseManifest_EnvAllowedWithoutGetEnv(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		perms := copyMap(m["permissions"].(map[string]any))
		perms["host_functions"] = []any{"log"}
		perms["env"] = map[string]any{
			"allowed":      true,
			"allowed_keys": []any{"HOME"},
		}
		m["permissions"] = perms
	})
	assertValidationError(t, data, "get_env")
}

// --------------------------------------------------------------------------
// memory / execution limits
// --------------------------------------------------------------------------

func TestParseManifest_MaxMemoryTooSmall(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		perms := copyMap(m["permissions"].(map[string]any))
		perms["max_memory_bytes"] = float64(65535)
		m["permissions"] = perms
	})
	assertValidationError(t, data, "max_memory_bytes")
}

func TestParseManifest_MaxExecutionZero(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		perms := copyMap(m["permissions"].(map[string]any))
		perms["max_execution_ms"] = float64(0)
		m["permissions"] = perms
	})
	assertValidationError(t, data, "max_execution_ms")
}

// --------------------------------------------------------------------------
// entrypoint
// --------------------------------------------------------------------------

func TestParseManifest_BadEntrypointType(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		ep := copyMap(m["entrypoint"].(map[string]any))
		ep["type"] = "unknown-type"
		m["entrypoint"] = ep
	})
	assertValidationError(t, data, "entrypoint.type")
}

func TestParseManifest_ModuleNotWasm(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		ep := copyMap(m["entrypoint"].(map[string]any))
		ep["module"] = "plugin.js"
		m["entrypoint"] = ep
	})
	assertValidationError(t, data, ".wasm")
}

func TestParseManifest_MissingExports(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		ep := copyMap(m["entrypoint"].(map[string]any))
		delete(ep, "exports")
		m["entrypoint"] = ep
	})
	assertValidationError(t, data, "entrypoint.exports")
}

// --------------------------------------------------------------------------
// engine
// --------------------------------------------------------------------------

func TestParseManifest_BadEngineRuntime(t *testing.T) {
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		m["engine"] = map[string]any{"runtime": "unknown-runtime"}
	})
	assertValidationError(t, data, "engine.runtime")
}

func TestParseManifest_InvalidJSON(t *testing.T) {
	_, err := plugins.ParseManifest([]byte("{bad json}"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseManifest_NotObject(t *testing.T) {
	_, err := plugins.ParseManifest([]byte(`["not", "an", "object"]`))
	if err == nil {
		t.Fatal("expected error when root is not an object")
	}
}

// --------------------------------------------------------------------------
// ValidationError structure
// --------------------------------------------------------------------------

func TestValidationError_MultipleIssues(t *testing.T) {
	// Trigger two distinct issues at once.
	data := mutate(t, validManifestJSON(), func(m map[string]any) {
		delete(m, "id")
		delete(m, "display_name")
	})
	var ve *plugins.ValidationError
	_, err := plugins.ParseManifest(data)
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if len(ve.Issues) < 2 {
		t.Errorf("expected ≥2 issues, got %d: %v", len(ve.Issues), ve.Issues)
	}
	// Error() string should contain a summary.
	if ve.Error() == "" {
		t.Error("Error() is empty")
	}
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func assertValidationError(t *testing.T, data []byte, mustContain string) {
	t.Helper()
	var ve *plugins.ValidationError
	_, err := plugins.ParseManifest(data)
	if err == nil {
		t.Fatalf("expected ValidationError containing %q, got nil", mustContain)
	}
	if !errors.As(err, &ve) {
		t.Fatalf("expected *plugins.ValidationError, got %T: %v", err, err)
	}
	for _, issue := range ve.Issues {
		if strings.Contains(issue, mustContain) {
			return
		}
	}
	t.Errorf("no issue contains %q; issues: %v", mustContain, ve.Issues)
}

func copyMap(m map[string]any) map[string]any {
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
