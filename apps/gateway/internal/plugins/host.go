package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// HookResult is the value returned by a plugin's hook export and threaded
// through the RunHooks pipeline.
type HookResult struct {
	Action  string          `json:"action"`            // "continue" or "reject"
	Payload json.RawMessage `json:"payload,omitempty"` // updated payload for next plugin
	Reason  string          `json:"reason,omitempty"`
}

// Executor is the interface satisfied by WasmExecutor (and by test mocks).
// It is the minimal surface the Host needs to drive a plugin.
type Executor interface {
	// Transform passes inputJSON through the plugin and returns transformed JSON.
	Transform(ctx context.Context, inputJSON []byte) ([]byte, error)
	// Hook calls the plugin's hook export for the given event and returns a
	// JSON-encoded HookResult.
	Hook(ctx context.Context, event string, payloadJSON []byte) ([]byte, error)
}

// ErrAlreadyRegistered is returned by Register when a plugin with the same ID
// has already been loaded.
var ErrAlreadyRegistered = errors.New("plugin already registered")

// ErrCapabilityUnsupported is returned by Register when the manifest declares
// a capability whose required WASM export is absent from the module.
var ErrCapabilityUnsupported = errors.New("plugin capability not supported by wasm module")

// ErrUnknownTarget is returned by Transform when the target string does not
// map to a known capability.
var ErrUnknownTarget = errors.New("unknown transform target")

// ErrUnknownEvent is returned by RunHooks when the event string does not map
// to a known capability.
var ErrUnknownEvent = errors.New("unknown hook event")

// targetCapability maps a transform target string to the corresponding
// Capability constant.
var targetCapability = map[string]Capability{
	"prompt":   CapabilityTransformPrompt,
	"response": CapabilityTransformResponse,
}

// eventCapability maps a hook event string to the corresponding Capability
// constant.
var eventCapability = map[string]Capability{
	"job.pre":           CapabilityHookJobPre,
	"job.post":          CapabilityHookJobPost,
	"webhook.transform": CapabilityHookWebhookTransform,
	"validate":          CapabilityHookValidate,
}

// HostOptions configures a Host.  The only required field is BuildExecutor.
type HostOptions struct {
	// BuildExecutor is called by Register to create an Executor for each plugin.
	// Production code passes NewWasmExecutorAdapter; tests inject a stub factory.
	BuildExecutor func(ctx context.Context, m Manifest, wasmBytes []byte) (Executor, error)
}

// pluginEntry bundles a manifest with its live executor.
type pluginEntry struct {
	manifest Manifest
	exec     Executor
}

// Host orchestrates a set of loaded WASM plugins.  It is goroutine-safe.
type Host struct {
	mu      sync.RWMutex
	entries []pluginEntry
	ids     map[string]struct{}
	build   func(ctx context.Context, m Manifest, wasmBytes []byte) (Executor, error)
}

// NewHost returns an initialised Host.  opts.BuildExecutor must not be nil.
func NewHost(opts HostOptions) *Host {
	if opts.BuildExecutor == nil {
		panic("plugins.NewHost: HostOptions.BuildExecutor must not be nil")
	}
	return &Host{
		ids:   make(map[string]struct{}),
		build: opts.BuildExecutor,
	}
}

// Register loads and validates a plugin, then adds it to the host.
//
// It returns ErrAlreadyRegistered if a plugin with the same ID was previously
// registered, and ErrCapabilityUnsupported if the manifest declares a
// capability whose required WASM export is absent.
func (h *Host) Register(ctx context.Context, m Manifest, wasmBytes []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.ids[m.ID]; exists {
		return fmt.Errorf("%w: %s", ErrAlreadyRegistered, m.ID)
	}

	exec, err := h.build(ctx, m, wasmBytes)
	if err != nil {
		return fmt.Errorf("plugin %s: build executor: %w", m.ID, err)
	}

	// Capability/export alignment check.
	needsTransform := false
	needsHook := false
	for _, cap := range m.Capabilities {
		s := string(cap)
		if strings.HasPrefix(s, "transform.") {
			needsTransform = true
		}
		if strings.HasPrefix(s, "hook.") {
			needsHook = true
		}
	}

	// Check export presence by calling HasTransform/HasHook if available,
	// falling back to the interface check via type assertion.
	if needsTransform {
		if !hasTransformExport(exec) {
			return fmt.Errorf("%w: plugin %s declares transform capability but wasm module lacks 'transform' export", ErrCapabilityUnsupported, m.ID)
		}
	}
	if needsHook {
		if !hasHookExport(exec) {
			return fmt.Errorf("%w: plugin %s declares hook capability but wasm module lacks 'hook' export", ErrCapabilityUnsupported, m.ID)
		}
	}

	h.entries = append(h.entries, pluginEntry{manifest: m, exec: exec})
	h.ids[m.ID] = struct{}{}
	return nil
}

// Transform runs the value through every plugin that declares the capability
// for the given target ("prompt" or "response"), in registration order.
//
// It returns ErrUnknownTarget for unrecognised targets.
func (h *Host) Transform(ctx context.Context, target string, valueJSON []byte) ([]byte, error) {
	cap, ok := targetCapability[target]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTarget, target)
	}

	h.mu.RLock()
	entries := h.entries // safe: slice header copy; entries are append-only
	h.mu.RUnlock()

	current := valueJSON
	for _, e := range entries {
		if !hasCapability(e.manifest.Capabilities, cap) {
			continue
		}
		out, err := e.exec.Transform(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("plugin %s transform: %w", e.manifest.ID, err)
		}
		if !json.Valid(out) {
			return nil, fmt.Errorf("plugin %s: transform returned invalid JSON", e.manifest.ID)
		}
		current = out
	}
	return current, nil
}

// RunHooks runs the payload through every plugin that declares the capability
// for the given event ("job.pre" or "job.post"), in registration order.
//
// Short-circuits and returns the first HookResult whose Action is "reject".
// Otherwise returns a HookResult{Action:"continue", Payload:<final payload>}.
//
// It returns ErrUnknownEvent for unrecognised events.
func (h *Host) RunHooks(ctx context.Context, event string, payloadJSON []byte) (*HookResult, error) {
	cap, ok := eventCapability[event]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEvent, event)
	}

	h.mu.RLock()
	entries := h.entries
	h.mu.RUnlock()

	current := payloadJSON
	for _, e := range entries {
		if !hasCapability(e.manifest.Capabilities, cap) {
			continue
		}
		raw, err := e.exec.Hook(ctx, event, current)
		if err != nil {
			return nil, fmt.Errorf("plugin %s hook: %w", e.manifest.ID, err)
		}

		var result HookResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("plugin %s: invalid hook result JSON: %w", e.manifest.ID, err)
		}

		if result.Action != "continue" && result.Action != "reject" {
			return nil, fmt.Errorf("plugin %s: unknown hook action %q", e.manifest.ID, result.Action)
		}

		if result.Action == "reject" {
			return &result, nil
		}

		// Thread the updated payload forward.
		if len(result.Payload) > 0 && !json.Valid(result.Payload) {
			return nil, fmt.Errorf("plugin %s: hook returned invalid payload JSON", e.manifest.ID)
		}
		if len(result.Payload) > 0 {
			current = result.Payload
		}
	}

	return &HookResult{Action: "continue", Payload: json.RawMessage(current)}, nil
}

// Has reports whether a plugin with the given ID is registered.
func (h *Host) Has(pluginID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.ids[pluginID]
	return ok
}

// List returns the manifests of all registered plugins in registration order.
func (h *Host) List() []Manifest {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Manifest, len(h.entries))
	for i, e := range h.entries {
		out[i] = e.manifest
	}
	return out
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// hasCapability reports whether caps contains target.
func hasCapability(caps []Capability, target Capability) bool {
	for _, c := range caps {
		if c == target {
			return true
		}
	}
	return false
}

// exportChecker is the optional interface executors may implement to report
// which WASM exports they found.  WasmExecutor implements this via the two
// helpers added below.
type exportChecker interface {
	HasTransform() bool
	HasHook() bool
}

func hasTransformExport(exec Executor) bool {
	if ec, ok := exec.(exportChecker); ok {
		return ec.HasTransform()
	}
	// If the executor doesn't implement exportChecker, assume present.
	return true
}

func hasHookExport(exec Executor) bool {
	if ec, ok := exec.(exportChecker); ok {
		return ec.HasHook()
	}
	return true
}

// NewWasmExecutorAdapter wraps NewWasmExecutor so it satisfies the Executor
// interface and can be used as HostOptions.BuildExecutor in production.
func NewWasmExecutorAdapter(ctx context.Context, m Manifest, wasmBytes []byte) (Executor, error) {
	return NewWasmExecutor(ctx, m.ID, m, wasmBytes)
}
