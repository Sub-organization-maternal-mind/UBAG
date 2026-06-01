// Package plugins provides manifest parsing, validation, and permission
// enforcement for the UBAG WASM plugin host.
//
// The Go types here mirror the TypeScript definitions in
// packages/plugins/src/manifest.ts.  ParseManifest is the single entry point:
// it deserialises raw JSON and returns a fully-validated Manifest or a
// ValidationError whose Issues slice contains every problem found.
package plugins

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// SchemaVersion is the only accepted value for the schema_version field.
const SchemaVersion = "ubag.plugin.v0"

// Capability names.
type Capability string

const (
	CapabilityTransformPrompt   Capability = "transform.prompt"
	CapabilityTransformResponse Capability = "transform.response"
	CapabilityHookJobPre        Capability = "hook.job.pre"
	CapabilityHookJobPost       Capability = "hook.job.post"
)

var knownCapabilities = map[Capability]struct{}{
	CapabilityTransformPrompt:   {},
	CapabilityTransformResponse: {},
	CapabilityHookJobPre:        {},
	CapabilityHookJobPost:       {},
}

// HostFunctionName represents a named host function a plugin may request.
type HostFunctionName string

const (
	HostFnLog     HostFunctionName = "log"
	HostFnClock   HostFunctionName = "clock"
	HostFnRandom  HostFunctionName = "random"
	HostFnFetch   HostFunctionName = "fetch"
	HostFnReadFile HostFunctionName = "read_file"
	HostFnGetEnv  HostFunctionName = "get_env"
)

var knownHostFunctions = map[HostFunctionName]struct{}{
	HostFnLog:     {},
	HostFnClock:   {},
	HostFnRandom:  {},
	HostFnFetch:   {},
	HostFnReadFile: {},
	HostFnGetEnv:  {},
}

// EntrypointType identifies the WASM component model variant.
type EntrypointType string

const (
	EntrypointWASIComponent EntrypointType = "wasi-component"
	EntrypointWASICommand   EntrypointType = "wasi-command"
	EntrypointCoreModule    EntrypointType = "core-module"
)

// EngineRuntime selects the WASM execution engine.
type EngineRuntime string

const (
	RuntimeWASIPreview1 EngineRuntime = "wasi-preview1"
	RuntimeWASIPreview2 EngineRuntime = "wasi-preview2"
	RuntimeCore         EngineRuntime = "core"
)

// -----------------------------------------------------------------------
// Struct types
// -----------------------------------------------------------------------

// NetworkPermission controls outbound HTTP/HTTPS access.
type NetworkPermission struct {
	Allowed      bool
	AllowedHosts []string
}

// FilesystemPermission controls read access to the host filesystem.
type FilesystemPermission struct {
	Allowed      bool
	AllowedPaths []string
}

// EnvPermission controls access to environment variables.
type EnvPermission struct {
	Allowed     bool
	AllowedKeys []string
}

// Permissions is the full permission set declared in a manifest.
type Permissions struct {
	HostFunctions  []HostFunctionName
	Network        NetworkPermission
	Filesystem     FilesystemPermission
	Env            EnvPermission
	MaxMemoryBytes int64
	MaxExecutionMS int64
}

// Entrypoint describes how the WASM module is loaded and called.
type Entrypoint struct {
	Type    EntrypointType
	Module  string
	Exports EntrypointExports
}

// EntrypointExports names the exported functions within the WASM module.
type EntrypointExports struct {
	Transform string // optional
	Hook      string // optional
	Init      string // optional
}

// Engine describes runtime requirements.
type Engine struct {
	Runtime        EngineRuntime
	MinHostVersion string // optional; "" means unset
}

// Manifest is a validated, fully-typed plugin manifest.
type Manifest struct {
	SchemaVersion string
	ID            string
	DisplayName   string
	Version       string
	Description   string // optional; "" means unset
	Capabilities  []Capability
	Entrypoint    Entrypoint
	Permissions   Permissions
	Engine        Engine
}

// -----------------------------------------------------------------------
// ValidationError
// -----------------------------------------------------------------------

// ValidationError is returned by ParseManifest when one or more fields are
// invalid.  Issues contains one human-readable string per problem.
type ValidationError struct {
	Issues []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("plugin manifest invalid: %s", strings.Join(e.Issues, "; "))
}

// -----------------------------------------------------------------------
// Compiled patterns
// -----------------------------------------------------------------------

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)
	semverPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+].+)?$`)
	strictSemver  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// -----------------------------------------------------------------------
// Constants for defaults and limits
// -----------------------------------------------------------------------

const (
	defaultMaxMemoryBytes int64 = 16_777_216
	defaultMaxExecutionMS int64 = 1_000
	minMemoryBytes        int64 = 65_536
	minExecutionMS        int64 = 1
)

// -----------------------------------------------------------------------
// ParseManifest
// -----------------------------------------------------------------------

// ParseManifest parses and validates a JSON-encoded plugin manifest.
// It returns a Manifest on success or a *ValidationError that lists every
// problem found.
func ParseManifest(data []byte) (Manifest, error) {
	// Step 1: JSON decode into a generic map.
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return Manifest{}, &ValidationError{Issues: []string{
			fmt.Sprintf("manifest is not valid JSON: %v", err),
		}}
	}

	record, ok := raw.(map[string]any)
	if !ok {
		return Manifest{}, &ValidationError{Issues: []string{
			"manifest must be a JSON object",
		}}
	}

	issues := &issueCollector{}

	// schema_version
	if sv, _ := record["schema_version"].(string); sv != SchemaVersion {
		issues.add(fmt.Sprintf("schema_version must be %q", SchemaVersion))
	}

	// id
	id, _ := record["id"].(string)
	if !idPattern.MatchString(id) {
		issues.add("id must match ^[a-z0-9][a-z0-9_-]{1,63}$")
	}

	// display_name
	displayName, _ := record["display_name"].(string)
	if displayName == "" {
		issues.add("display_name must be a non-empty string")
	}

	// version
	version, _ := record["version"].(string)
	if !semverPattern.MatchString(version) {
		issues.add("version must be a semantic version string")
	}

	// description (optional)
	var description string
	if rawDesc, exists := record["description"]; exists {
		if s, ok := rawDesc.(string); ok {
			description = s
		} else if rawDesc != nil {
			issues.add("description must be a string")
		}
	}

	// capabilities
	caps := parseCapabilities(record["capabilities"], issues)

	// entrypoint
	ep := parseEntrypoint(record["entrypoint"], issues)

	// permissions
	perms := parsePermissions(record["permissions"], issues)

	// engine
	eng := parseEngine(record["engine"], issues)

	if len(issues.list) > 0 {
		return Manifest{}, &ValidationError{Issues: issues.list}
	}

	return Manifest{
		SchemaVersion: SchemaVersion,
		ID:            id,
		DisplayName:   displayName,
		Version:       version,
		Description:   description,
		Capabilities:  caps,
		Entrypoint:    ep,
		Permissions:   perms,
		Engine:        eng,
	}, nil
}

// -----------------------------------------------------------------------
// Sub-parsers
// -----------------------------------------------------------------------

func parseCapabilities(raw any, issues *issueCollector) []Capability {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		issues.add("capabilities must be a non-empty array")
		return nil
	}
	seen := map[Capability]struct{}{}
	out := make([]Capability, 0, len(arr))
	for _, entry := range arr {
		s, ok := entry.(string)
		if !ok {
			issues.addUnique(fmt.Sprintf(`capabilities contains unknown capability "%v"`, entry))
			continue
		}
		cap := Capability(s)
		if _, known := knownCapabilities[cap]; !known {
			issues.addUnique(fmt.Sprintf(`capabilities contains unknown capability "%s"`, s))
			continue
		}
		if _, dup := seen[cap]; !dup {
			seen[cap] = struct{}{}
			out = append(out, cap)
		}
	}
	return out
}

func parseHostFunctions(raw any, issues *issueCollector) []HostFunctionName {
	arr, ok := raw.([]any)
	if !ok {
		issues.add("permissions.host_functions must be an array")
		return nil
	}
	seen := map[HostFunctionName]struct{}{}
	out := make([]HostFunctionName, 0, len(arr))
	for _, entry := range arr {
		s, ok := entry.(string)
		if !ok {
			issues.addUnique(fmt.Sprintf(`permissions.host_functions contains unknown host function "%v"`, entry))
			continue
		}
		fn := HostFunctionName(s)
		if _, known := knownHostFunctions[fn]; !known {
			issues.addUnique(fmt.Sprintf(`permissions.host_functions contains unknown host function "%s"`, s))
			continue
		}
		if _, dup := seen[fn]; !dup {
			seen[fn] = struct{}{}
			out = append(out, fn)
		}
	}
	return out
}

// parsePermissionToggle parses a network / filesystem / env sub-object.
// listKey is the name of the string-array field ("allowed_hosts" etc.)
func parsePermissionToggle(raw any, field, listKey string, issues *issueCollector) (allowed bool, list []string) {
	if raw == nil {
		return false, nil
	}
	rec, ok := raw.(map[string]any)
	if !ok {
		issues.addUnique(fmt.Sprintf("permissions.%s must be an object", field))
		return false, nil
	}

	if b, ok := rec["allowed"].(bool); ok {
		allowed = b
	} else {
		issues.addUnique(fmt.Sprintf("permissions.%s.allowed must be a boolean", field))
	}

	if rawList, exists := rec[listKey]; exists {
		arr, ok := rawList.([]any)
		if !ok {
			issues.addUnique(fmt.Sprintf("permissions.%s.%s must be a string array", field, listKey))
		} else {
			for _, item := range arr {
				s, ok := item.(string)
				if !ok {
					issues.addUnique(fmt.Sprintf("permissions.%s.%s must be a string array", field, listKey))
					break
				}
				list = append(list, s)
			}
		}
	}

	if !allowed && len(list) > 0 {
		issues.addUnique(fmt.Sprintf(
			"permissions.%s.%s is set but permissions.%s.allowed is false",
			field, listKey, field))
	}
	return allowed, list
}

func parsePermissions(raw any, issues *issueCollector) Permissions {
	rec, ok := raw.(map[string]any)
	if !ok {
		issues.addUnique("permissions must be an object")
		rec = map[string]any{}
	}

	hostFns := parseHostFunctions(rec["host_functions"], issues)

	netAllowed, netHosts := parsePermissionToggle(rec["network"], "network", "allowed_hosts", issues)
	fsAllowed, fsPaths := parsePermissionToggle(rec["filesystem"], "filesystem", "allowed_paths", issues)
	envAllowed, envKeys := parsePermissionToggle(rec["env"], "env", "allowed_keys", issues)

	// Cross-checks: capability must imply the required host function.
	if netAllowed && !containsHostFn(hostFns, HostFnFetch) {
		issues.addUnique(`permissions.network.allowed requires host function "fetch"`)
	}
	if fsAllowed && !containsHostFn(hostFns, HostFnReadFile) {
		issues.addUnique(`permissions.filesystem.allowed requires host function "read_file"`)
	}
	if envAllowed && !containsHostFn(hostFns, HostFnGetEnv) {
		issues.addUnique(`permissions.env.allowed requires host function "get_env"`)
	}

	maxMem := parsePositiveInt(rec["max_memory_bytes"], "permissions.max_memory_bytes", defaultMaxMemoryBytes, minMemoryBytes, issues)
	maxExec := parsePositiveInt(rec["max_execution_ms"], "permissions.max_execution_ms", defaultMaxExecutionMS, minExecutionMS, issues)

	return Permissions{
		HostFunctions: hostFns,
		Network:       NetworkPermission{Allowed: netAllowed, AllowedHosts: netHosts},
		Filesystem:    FilesystemPermission{Allowed: fsAllowed, AllowedPaths: fsPaths},
		Env:           EnvPermission{Allowed: envAllowed, AllowedKeys: envKeys},
		MaxMemoryBytes: maxMem,
		MaxExecutionMS: maxExec,
	}
}

func parsePositiveInt(raw any, field string, fallback, minimum int64, issues *issueCollector) int64 {
	if raw == nil {
		return fallback
	}
	f, ok := raw.(float64)
	if !ok || f != float64(int64(f)) || int64(f) < minimum {
		issues.addUnique(fmt.Sprintf("%s must be an integer >= %d", field, minimum))
		return fallback
	}
	return int64(f)
}

func parseEntrypoint(raw any, issues *issueCollector) Entrypoint {
	rec, ok := raw.(map[string]any)
	if !ok {
		issues.addUnique("entrypoint must be an object")
		return Entrypoint{}
	}

	epType, _ := rec["type"].(string)
	switch EntrypointType(epType) {
	case EntrypointWASIComponent, EntrypointWASICommand, EntrypointCoreModule:
		// valid
	default:
		issues.addUnique("entrypoint.type must be one of wasi-component, wasi-command, core-module")
	}

	module, _ := rec["module"].(string)
	if !strings.HasSuffix(module, ".wasm") {
		issues.addUnique("entrypoint.module must be a path ending in .wasm")
	}

	var exports EntrypointExports
	if rawExports, ok := rec["exports"].(map[string]any); ok {
		if s, ok := rawExports["transform"].(string); ok && s != "" {
			exports.Transform = s
		} else if rawExports["transform"] != nil {
			issues.addUnique("entrypoint.exports.transform must be a non-empty string")
		}
		if s, ok := rawExports["hook"].(string); ok && s != "" {
			exports.Hook = s
		} else if rawExports["hook"] != nil {
			issues.addUnique("entrypoint.exports.hook must be a non-empty string")
		}
		if s, ok := rawExports["init"].(string); ok && s != "" {
			exports.Init = s
		} else if rawExports["init"] != nil {
			issues.addUnique("entrypoint.exports.init must be a non-empty string")
		}
	} else if rec["exports"] != nil {
		issues.addUnique("entrypoint.exports must be an object")
	}

	return Entrypoint{
		Type:    EntrypointType(epType),
		Module:  module,
		Exports: exports,
	}
}

func parseEngine(raw any, issues *issueCollector) Engine {
	rec, ok := raw.(map[string]any)
	if !ok {
		issues.addUnique("engine must be an object")
		return Engine{}
	}

	runtime, _ := rec["runtime"].(string)
	switch EngineRuntime(runtime) {
	case RuntimeWASIPreview1, RuntimeWASIPreview2, RuntimeCore:
		// valid
	default:
		issues.addUnique("engine.runtime must be one of wasi-preview1, wasi-preview2, core")
	}

	var minHostVersion string
	if rawMin, exists := rec["min_host_version"]; exists && rawMin != nil {
		if s, ok := rawMin.(string); ok {
			if !strictSemver.MatchString(s) {
				issues.addUnique("engine.min_host_version must be a MAJOR.MINOR.PATCH string")
			} else {
				minHostVersion = s
			}
		} else {
			issues.addUnique("engine.min_host_version must be a MAJOR.MINOR.PATCH string")
		}
	}

	return Engine{
		Runtime:        EngineRuntime(runtime),
		MinHostVersion: minHostVersion,
	}
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func containsHostFn(fns []HostFunctionName, target HostFunctionName) bool {
	for _, fn := range fns {
		if fn == target {
			return true
		}
	}
	return false
}

// issueCollector accumulates unique validation issue strings.
type issueCollector struct {
	list []string
	set  map[string]struct{}
}

func (ic *issueCollector) add(msg string) {
	if ic.set == nil {
		ic.set = make(map[string]struct{})
	}
	ic.list = append(ic.list, msg)
	ic.set[msg] = struct{}{}
}

func (ic *issueCollector) addUnique(msg string) {
	if ic.set == nil {
		ic.set = make(map[string]struct{})
	}
	if _, exists := ic.set[msg]; !exists {
		ic.set[msg] = struct{}{}
		ic.list = append(ic.list, msg)
	}
}
