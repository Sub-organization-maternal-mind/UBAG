package plugins_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/plugins"
)

// -----------------------------------------------------------------------
// Mock executor helpers
// -----------------------------------------------------------------------

// mockExecutor is a test double for plugins.Executor.
// It records calls and returns configurable responses.
type mockExecutor struct {
	// transformFn is called by Transform; if nil, Transform returns the input unchanged.
	transformFn func(ctx context.Context, inputJSON []byte) ([]byte, error)
	// hookFn is called by Hook; if nil, Hook returns {"action":"continue"}.
	hookFn func(ctx context.Context, event string, payloadJSON []byte) ([]byte, error)
	// callsTransform counts how many times Transform was invoked.
	callsTransform int
	// callsHook counts how many times Hook was invoked.
	callsHook int
	// hasTransform controls whether HasTransform() returns true.
	hasTransform bool
	// hasHook controls whether HasHook() returns true.
	hasHook bool
}

func (m *mockExecutor) Transform(ctx context.Context, inputJSON []byte) ([]byte, error) {
	m.callsTransform++
	if m.transformFn != nil {
		return m.transformFn(ctx, inputJSON)
	}
	// Default: echo input.
	return inputJSON, nil
}

func (m *mockExecutor) Hook(ctx context.Context, event string, payloadJSON []byte) ([]byte, error) {
	m.callsHook++
	if m.hookFn != nil {
		return m.hookFn(ctx, event, payloadJSON)
	}
	// Default: continue with unchanged payload.
	return json.Marshal(plugins.HookResult{Action: "continue", Payload: payloadJSON})
}

// HasTransform implements exportChecker interface checked in host.go.
func (m *mockExecutor) HasTransform() bool { return m.hasTransform }

// HasHook implements exportChecker interface checked in host.go.
func (m *mockExecutor) HasHook() bool { return m.hasHook }

// makeFactory returns a BuildExecutor factory that hands out pre-built mock
// executors in order.  It panics if more executors are requested than provided.
func makeFactory(mocks ...*mockExecutor) func(ctx context.Context, m plugins.Manifest, wasmBytes []byte) (plugins.Executor, error) {
	idx := 0
	return func(ctx context.Context, m plugins.Manifest, wasmBytes []byte) (plugins.Executor, error) {
		if idx >= len(mocks) {
			return nil, fmt.Errorf("makeFactory: unexpected Register call #%d", idx+1)
		}
		exec := mocks[idx]
		idx++
		return exec, nil
	}
}

// makeManifest is a minimal but valid Manifest builder for tests.
func makeManifest(id string, caps ...plugins.Capability) plugins.Manifest {
	return plugins.Manifest{
		SchemaVersion: plugins.SchemaVersion,
		ID:            id,
		DisplayName:   id,
		Version:       "0.1.0",
		Capabilities:  caps,
		Entrypoint: plugins.Entrypoint{
			Type:   plugins.EntrypointCoreModule,
			Module: "test.wasm",
		},
		Engine: plugins.Engine{Runtime: plugins.RuntimeCore},
		Permissions: plugins.Permissions{
			MaxMemoryBytes: 1 * 1024 * 1024,
			MaxExecutionMS: 1000,
		},
	}
}

// -----------------------------------------------------------------------
// Test 1: Transform chain composes
// -----------------------------------------------------------------------

// TestTransformChainComposes verifies that two transform plugins are applied in
// registration order and each plugin's output becomes the next plugin's input.
func TestTransformChainComposes(t *testing.T) {
	ctx := context.Background()

	// Plugin A appends "A" to a JSON string value.
	pluginA := &mockExecutor{
		hasTransform: true,
		transformFn: func(_ context.Context, inputJSON []byte) ([]byte, error) {
			var s string
			if err := json.Unmarshal(inputJSON, &s); err != nil {
				return nil, err
			}
			return json.Marshal(s + "A")
		},
	}

	// Plugin B appends "B" to a JSON string value.
	pluginB := &mockExecutor{
		hasTransform: true,
		transformFn: func(_ context.Context, inputJSON []byte) ([]byte, error) {
			var s string
			if err := json.Unmarshal(inputJSON, &s); err != nil {
				return nil, err
			}
			return json.Marshal(s + "B")
		},
	}

	host := plugins.NewHost(plugins.HostOptions{
		BuildExecutor: makeFactory(pluginA, pluginB),
	})

	mA := makeManifest("plugin-a", plugins.CapabilityTransformPrompt)
	mB := makeManifest("plugin-b", plugins.CapabilityTransformPrompt)

	if err := host.Register(ctx, mA, nil); err != nil {
		t.Fatalf("Register plugin-a: %v", err)
	}
	if err := host.Register(ctx, mB, nil); err != nil {
		t.Fatalf("Register plugin-b: %v", err)
	}

	input, _ := json.Marshal("hello")
	out, err := host.Transform(ctx, "prompt", input)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	const want = "helloAB"
	if got != want {
		t.Errorf("chain result = %q; want %q", got, want)
	}

	if pluginA.callsTransform != 1 {
		t.Errorf("pluginA.callsTransform = %d; want 1", pluginA.callsTransform)
	}
	if pluginB.callsTransform != 1 {
		t.Errorf("pluginB.callsTransform = %d; want 1", pluginB.callsTransform)
	}
}

// -----------------------------------------------------------------------
// Test 2: Rejecting pre-job hook short-circuits
// -----------------------------------------------------------------------

// TestRejectHookShortCircuits verifies that once a plugin returns
// action:"reject", RunHooks stops immediately and no later plugins are called.
func TestRejectHookShortCircuits(t *testing.T) {
	ctx := context.Background()

	// Plugin A rejects with a reason.
	pluginA := &mockExecutor{
		hasHook: true,
		hookFn: func(_ context.Context, event string, payloadJSON []byte) ([]byte, error) {
			return json.Marshal(plugins.HookResult{
				Action: "reject",
				Reason: "blocked by policy",
			})
		},
	}

	// Plugin B should never be called.
	pluginB := &mockExecutor{
		hasHook: true,
		hookFn: func(_ context.Context, event string, payloadJSON []byte) ([]byte, error) {
			return json.Marshal(plugins.HookResult{Action: "continue", Payload: payloadJSON})
		},
	}

	host := plugins.NewHost(plugins.HostOptions{
		BuildExecutor: makeFactory(pluginA, pluginB),
	})

	mA := makeManifest("plugin-a", plugins.CapabilityHookJobPre)
	mB := makeManifest("plugin-b", plugins.CapabilityHookJobPre)

	if err := host.Register(ctx, mA, nil); err != nil {
		t.Fatalf("Register plugin-a: %v", err)
	}
	if err := host.Register(ctx, mB, nil); err != nil {
		t.Fatalf("Register plugin-b: %v", err)
	}

	payload, _ := json.Marshal(map[string]string{"user": "alice"})
	result, err := host.RunHooks(ctx, "job.pre", payload)
	if err != nil {
		t.Fatalf("RunHooks: %v", err)
	}

	if result.Action != "reject" {
		t.Errorf("action = %q; want \"reject\"", result.Action)
	}
	if result.Reason != "blocked by policy" {
		t.Errorf("reason = %q; want \"blocked by policy\"", result.Reason)
	}

	// Plugin B must never have been called.
	if pluginB.callsHook != 0 {
		t.Errorf("pluginB.callsHook = %d; want 0 (should have been short-circuited)", pluginB.callsHook)
	}
}

// -----------------------------------------------------------------------
// Test 3: Capability filtering selects the right plugins
// -----------------------------------------------------------------------

// TestCapabilityFilteringSelectsRightPlugins registers one transform plugin and
// one hook plugin, then verifies:
//   - Transform only calls the transform plugin (not the hook plugin).
//   - RunHooks only calls the hook plugin (not the transform plugin).
func TestCapabilityFilteringSelectsRightPlugins(t *testing.T) {
	ctx := context.Background()

	transformPlugin := &mockExecutor{hasTransform: true}
	hookPlugin := &mockExecutor{hasHook: true}

	host := plugins.NewHost(plugins.HostOptions{
		BuildExecutor: makeFactory(transformPlugin, hookPlugin),
	})

	mTransform := makeManifest("transform-plugin", plugins.CapabilityTransformPrompt)
	mHook := makeManifest("hook-plugin", plugins.CapabilityHookJobPost)

	if err := host.Register(ctx, mTransform, nil); err != nil {
		t.Fatalf("Register transform-plugin: %v", err)
	}
	if err := host.Register(ctx, mHook, nil); err != nil {
		t.Fatalf("Register hook-plugin: %v", err)
	}

	// Run transform — only the transform plugin should be called.
	input, _ := json.Marshal("test")
	if _, err := host.Transform(ctx, "prompt", input); err != nil {
		t.Fatalf("Transform: %v", err)
	}

	if transformPlugin.callsTransform != 1 {
		t.Errorf("transformPlugin.callsTransform = %d; want 1", transformPlugin.callsTransform)
	}
	if hookPlugin.callsTransform != 0 {
		t.Errorf("hookPlugin.callsTransform = %d; want 0 (wrong capability)", hookPlugin.callsTransform)
	}

	// Reset call counts for hook test.
	transformPlugin.callsTransform = 0

	// Run hooks — only the hook plugin should be called.
	payload, _ := json.Marshal(map[string]string{"k": "v"})
	result, err := host.RunHooks(ctx, "job.post", payload)
	if err != nil {
		t.Fatalf("RunHooks: %v", err)
	}

	if result.Action != "continue" {
		t.Errorf("action = %q; want \"continue\"", result.Action)
	}
	if hookPlugin.callsHook != 1 {
		t.Errorf("hookPlugin.callsHook = %d; want 1", hookPlugin.callsHook)
	}
	if transformPlugin.callsHook != 0 {
		t.Errorf("transformPlugin.callsHook = %d; want 0 (wrong capability)", transformPlugin.callsHook)
	}
}

// -----------------------------------------------------------------------
// Test 4: Duplicate registration rejected
// -----------------------------------------------------------------------

// TestDuplicateRegistrationRejected verifies that registering a plugin ID
// twice returns ErrAlreadyRegistered on the second call.
func TestDuplicateRegistrationRejected(t *testing.T) {
	ctx := context.Background()

	ex1 := &mockExecutor{hasTransform: true}
	ex2 := &mockExecutor{hasTransform: true}

	host := plugins.NewHost(plugins.HostOptions{
		BuildExecutor: makeFactory(ex1, ex2),
	})

	m := makeManifest("my-plugin", plugins.CapabilityTransformPrompt)

	if err := host.Register(ctx, m, nil); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	err := host.Register(ctx, m, nil)
	if err == nil {
		t.Fatal("second Register: expected error, got nil")
	}
	if !errors.Is(err, plugins.ErrAlreadyRegistered) {
		t.Errorf("expected ErrAlreadyRegistered, got: %v", err)
	}
}

// -----------------------------------------------------------------------
// Test 5: Has and List
// -----------------------------------------------------------------------

func TestHostHasAndList(t *testing.T) {
	ctx := context.Background()

	ex := &mockExecutor{hasTransform: true}
	host := plugins.NewHost(plugins.HostOptions{
		BuildExecutor: makeFactory(ex),
	})

	if host.Has("p1") {
		t.Fatal("Has('p1') should be false before registration")
	}

	m := makeManifest("p1", plugins.CapabilityTransformResponse)
	if err := host.Register(ctx, m, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if !host.Has("p1") {
		t.Fatal("Has('p1') should be true after registration")
	}

	list := host.List()
	if len(list) != 1 {
		t.Fatalf("List() len = %d; want 1", len(list))
	}
	if list[0].ID != "p1" {
		t.Errorf("List()[0].ID = %q; want \"p1\"", list[0].ID)
	}
}

// -----------------------------------------------------------------------
// Test 6: Hook payload threading
// -----------------------------------------------------------------------

// TestHookPayloadThreaded verifies that when a plugin returns action:"continue"
// with an updated payload, the next plugin receives that updated payload.
func TestHookPayloadThreaded(t *testing.T) {
	ctx := context.Background()

	// Plugin A adds a field to the JSON object.
	pluginA := &mockExecutor{
		hasHook: true,
		hookFn: func(_ context.Context, event string, payloadJSON []byte) ([]byte, error) {
			var m map[string]string
			if err := json.Unmarshal(payloadJSON, &m); err != nil {
				return nil, err
			}
			m["a"] = "from-plugin-a"
			updated, _ := json.Marshal(m)
			return json.Marshal(plugins.HookResult{Action: "continue", Payload: updated})
		},
	}

	// Plugin B records the payload it received and adds another field.
	var receivedByB []byte
	pluginB := &mockExecutor{
		hasHook: true,
		hookFn: func(_ context.Context, event string, payloadJSON []byte) ([]byte, error) {
			receivedByB = make([]byte, len(payloadJSON))
			copy(receivedByB, payloadJSON)
			var m map[string]string
			if err := json.Unmarshal(payloadJSON, &m); err != nil {
				return nil, err
			}
			m["b"] = "from-plugin-b"
			updated, _ := json.Marshal(m)
			return json.Marshal(plugins.HookResult{Action: "continue", Payload: updated})
		},
	}

	host := plugins.NewHost(plugins.HostOptions{
		BuildExecutor: makeFactory(pluginA, pluginB),
	})

	mA := makeManifest("plugin-a", plugins.CapabilityHookJobPre)
	mB := makeManifest("plugin-b", plugins.CapabilityHookJobPre)

	if err := host.Register(ctx, mA, nil); err != nil {
		t.Fatalf("Register plugin-a: %v", err)
	}
	if err := host.Register(ctx, mB, nil); err != nil {
		t.Fatalf("Register plugin-b: %v", err)
	}

	initial, _ := json.Marshal(map[string]string{"original": "yes"})
	result, err := host.RunHooks(ctx, "job.pre", initial)
	if err != nil {
		t.Fatalf("RunHooks: %v", err)
	}

	if result.Action != "continue" {
		t.Errorf("action = %q; want \"continue\"", result.Action)
	}

	// Plugin B should have received the payload that A produced.
	var bReceived map[string]string
	if err := json.Unmarshal(receivedByB, &bReceived); err != nil {
		t.Fatalf("unmarshal receivedByB: %v", err)
	}
	if bReceived["a"] != "from-plugin-a" {
		t.Errorf("plugin B did not receive plugin A's output; got %v", bReceived)
	}

	// Final result should have both fields.
	var final map[string]string
	if err := json.Unmarshal(result.Payload, &final); err != nil {
		t.Fatalf("unmarshal final payload: %v", err)
	}
	if final["a"] != "from-plugin-a" || final["b"] != "from-plugin-b" {
		t.Errorf("final payload missing fields; got %v", final)
	}
}
