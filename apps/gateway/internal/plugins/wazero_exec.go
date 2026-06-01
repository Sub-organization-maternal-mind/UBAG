package plugins

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// WasmExecutor holds a compiled WASM module together with its wazero runtime
// and enforces the permission and resource limits declared in the Manifest.
//
// # Lifecycle
//
// Create one WasmExecutor per plugin instance via NewWasmExecutor.  Call
// Transform or Hook for each invocation.  When the plugin is unloaded, call
// Close to release wazero resources.
//
// # Thread safety
//
// WasmExecutor is NOT goroutine-safe.  The caller must serialise concurrent
// calls, or create a separate WasmExecutor per goroutine.
type WasmExecutor struct {
	rt       wazero.Runtime
	mod      api.Module
	manifest Manifest
}

// NewWasmExecutor compiles wasmBytes and instantiates the module under a
// freshly-created wazero runtime configured for the given Manifest.
//
// The host module "env" is populated only with functions whose name appears in
// manifest.Permissions.HostFunctions.  If the guest module declares an import
// that is absent from the host module, wazero returns an error at
// instantiation time (the "denied import traps" contract).
//
// Memory is bounded by manifest.Permissions.MaxMemoryBytes rounded down to the
// nearest 64 KiB page.  If the WASM module requests more pages at minimum than
// the limit allows, instantiation fails.
func NewWasmExecutor(ctx context.Context, pluginID string, manifest Manifest, wasmBytes []byte) (*WasmExecutor, error) {
	maxPages := uint32(manifest.Permissions.MaxMemoryBytes / 65536)
	if maxPages == 0 {
		maxPages = 1
	}

	rtCfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(maxPages).
		WithCloseOnContextDone(true)

	rt := wazero.NewRuntimeWithConfig(ctx, rtCfg)

	checker := NewPermissionsChecker(manifest.Permissions)

	// Build the "env" host module, registering only the allowed host functions.
	if err := buildEnvModule(ctx, rt, checker, manifest); err != nil {
		rt.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("plugin %s: build env module: %w", pluginID, err)
	}

	// Compile and instantiate the guest module.
	compiled, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		rt.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("plugin %s: compile: %w", pluginID, err)
	}

	mod, err := rt.InstantiateModule(ctx, compiled,
		wazero.NewModuleConfig().WithName(pluginID).WithStartFunctions())
	if err != nil {
		rt.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("plugin %s: instantiate: %w", pluginID, err)
	}

	return &WasmExecutor{
		rt:       rt,
		mod:      mod,
		manifest: manifest,
	}, nil
}

// Close releases the wazero runtime and all modules it holds.
func (e *WasmExecutor) Close(ctx context.Context) error {
	return e.rt.Close(ctx)
}

// Transform calls the guest's alloc + transform exports using the v1 ABI:
//
//  1. alloc(len) → ptr
//  2. Write inputJSON to guest memory at ptr.
//  3. transform(ptr, len) → packed u64 where resultPtr = packed>>32, resultLen = packed&0xFFFFFFFF
//  4. Read resultLen bytes from guest memory at resultPtr.
//
// A per-call context is derived with the deadline configured in the Manifest.
func (e *WasmExecutor) Transform(ctx context.Context, inputJSON []byte) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx,
		time.Duration(e.manifest.Permissions.MaxExecutionMS)*time.Millisecond)
	defer cancel()

	return e.callTransform(callCtx, inputJSON)
}

// Hook calls the guest's alloc + hook exports using the v1 ABI:
//
//  1. alloc(eventLen) → eventPtr
//  2. Write event bytes into guest memory.
//  3. alloc(payloadLen) → payloadPtr
//  4. Write payloadJSON into guest memory.
//  5. hook(eventPtr, eventLen, payloadPtr, payloadLen) → packed u64
//  6. Read result from guest memory.
func (e *WasmExecutor) Hook(ctx context.Context, event string, payloadJSON []byte) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx,
		time.Duration(e.manifest.Permissions.MaxExecutionMS)*time.Millisecond)
	defer cancel()

	return e.callHook(callCtx, event, payloadJSON)
}

// -----------------------------------------------------------------------
// Internal call helpers
// -----------------------------------------------------------------------

func (e *WasmExecutor) callTransform(ctx context.Context, input []byte) ([]byte, error) {
	mem := e.mod.Memory()
	if mem == nil {
		return nil, fmt.Errorf("module has no memory")
	}

	// 1. alloc
	ptr, err := e.alloc(ctx, uint32(len(input)))
	if err != nil {
		return nil, fmt.Errorf("alloc: %w", err)
	}

	// 2. write input
	if !mem.Write(ptr, input) {
		return nil, fmt.Errorf("write input to guest memory failed (ptr=%d len=%d)", ptr, len(input))
	}

	// 3. call transform
	xform := e.mod.ExportedFunction("transform")
	if xform == nil {
		return nil, fmt.Errorf("module does not export 'transform'")
	}
	res, err := xform.Call(ctx, uint64(ptr), uint64(len(input)))
	if err != nil {
		return nil, fmt.Errorf("transform call: %w", err)
	}

	// 4. unpack result
	return e.readPackedResult(mem, res[0])
}

func (e *WasmExecutor) callHook(ctx context.Context, event string, payload []byte) ([]byte, error) {
	mem := e.mod.Memory()
	if mem == nil {
		return nil, fmt.Errorf("module has no memory")
	}

	eventBytes := []byte(event)

	eventPtr, err := e.alloc(ctx, uint32(len(eventBytes)))
	if err != nil {
		return nil, fmt.Errorf("alloc event: %w", err)
	}
	if !mem.Write(eventPtr, eventBytes) {
		return nil, fmt.Errorf("write event to guest memory failed")
	}

	payloadPtr, err := e.alloc(ctx, uint32(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("alloc payload: %w", err)
	}
	if !mem.Write(payloadPtr, payload) {
		return nil, fmt.Errorf("write payload to guest memory failed")
	}

	hookFn := e.mod.ExportedFunction("hook")
	if hookFn == nil {
		return nil, fmt.Errorf("module does not export 'hook'")
	}
	res, err := hookFn.Call(ctx, uint64(eventPtr), uint64(len(eventBytes)), uint64(payloadPtr), uint64(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("hook call: %w", err)
	}

	return e.readPackedResult(mem, res[0])
}

// alloc calls the guest's exported alloc function and returns the pointer.
func (e *WasmExecutor) alloc(ctx context.Context, size uint32) (uint32, error) {
	allocFn := e.mod.ExportedFunction("alloc")
	if allocFn == nil {
		return 0, fmt.Errorf("module does not export 'alloc'")
	}
	res, err := allocFn.Call(ctx, uint64(size))
	if err != nil {
		return 0, err
	}
	return uint32(res[0]), nil
}

// readPackedResult decodes a packed u64 (resultPtr<<32 | resultLen) and reads
// the corresponding bytes from guest memory.
func (e *WasmExecutor) readPackedResult(mem api.Memory, packed uint64) ([]byte, error) {
	resPtr := uint32(packed >> 32)
	resLen := uint32(packed & 0xFFFFFFFF)
	if resLen == 0 {
		return []byte{}, nil
	}
	out, ok := mem.Read(resPtr, resLen)
	if !ok {
		return nil, fmt.Errorf("read result from guest memory failed (ptr=%d len=%d)", resPtr, resLen)
	}
	// Read returns a view into wasm memory; copy to avoid aliasing.
	result := make([]byte, resLen)
	copy(result, out)
	return result, nil
}

// -----------------------------------------------------------------------
// Host module construction
// -----------------------------------------------------------------------

// buildEnvModule registers the "env" host module with only the host functions
// that the manifest allows.  Functions absent from the allowlist are simply not
// registered; if the guest imports them, wazero will fail to link them at
// instantiation time, which is the desired "trap on denied import" behaviour.
func buildEnvModule(ctx context.Context, rt wazero.Runtime, checker PermissionsChecker, manifest Manifest) error {
	builder := rt.NewHostModuleBuilder("env")

	if checker.AllowsHostFunction(HostFnLog) {
		builder.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(hostLog),
				[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, nil).
			Export("log")
	}

	if checker.AllowsHostFunction(HostFnFetch) {
		builder.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(makeHostFetch(checker)),
				[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
				[]api.ValueType{api.ValueTypeI32}).
			Export("fetch")
	}

	if checker.AllowsHostFunction(HostFnReadFile) {
		builder.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(makeHostReadFile(checker)),
				[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
				[]api.ValueType{api.ValueTypeI32}).
			Export("read_file")
	}

	if checker.AllowsHostFunction(HostFnGetEnv) {
		builder.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(makeHostGetEnv(checker)),
				[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
				[]api.ValueType{api.ValueTypeI32}).
			Export("get_env")
	}

	if checker.AllowsHostFunction(HostFnClock) {
		builder.NewFunctionBuilder().
			WithGoFunction(api.GoFunc(hostClock),
				[]api.ValueType{},
				[]api.ValueType{api.ValueTypeI64}).
			Export("clock")
	}

	if checker.AllowsHostFunction(HostFnRandom) {
		builder.NewFunctionBuilder().
			WithGoFunction(api.GoFunc(hostRandom),
				[]api.ValueType{api.ValueTypeI32},
				[]api.ValueType{api.ValueTypeI64}).
			Export("random")
	}

	_, err := builder.Instantiate(ctx)
	return err
}

// -----------------------------------------------------------------------
// Host function implementations
// -----------------------------------------------------------------------

// hostLog writes a UTF-8 message to stderr.
// Signature: (ptr i32, len i32) → void
func hostLog(_ context.Context, mod api.Module, stack []uint64) {
	ptr := api.DecodeU32(stack[0])
	length := api.DecodeU32(stack[1])
	if msg, ok := mod.Memory().Read(ptr, length); ok {
		os.Stderr.Write(msg) //nolint:errcheck
		os.Stderr.WriteString("\n") //nolint:errcheck
	}
}

// hostClock returns milliseconds since the Unix epoch.
// Signature: () → i64
func hostClock(_ context.Context, stack []uint64) {
	stack[0] = uint64(time.Now().UnixMilli())
}

// hostRandom returns a pseudo-random 64-bit value.
// Signature: (seed i32) → i64
func hostRandom(_ context.Context, stack []uint64) {
	// Use the current time as entropy source — good enough for plugin use.
	stack[0] = uint64(time.Now().UnixNano())
}

// makeHostFetch builds the fetch host function with network allowlist enforcement.
// Signature: (urlPtr i32, urlLen i32, outPtr i32, outLen i32) → i32 (bytes written or -1 on deny)
func makeHostFetch(checker PermissionsChecker) api.GoModuleFunc {
	return func(_ context.Context, mod api.Module, stack []uint64) {
		urlPtr := api.DecodeU32(stack[0])
		urlLen := api.DecodeU32(stack[1])
		mem := mod.Memory()

		urlBytes, ok := mem.Read(urlPtr, urlLen)
		if !ok {
			stack[0] = api.EncodeI32(-1)
			return
		}
		url := string(urlBytes)
		if !checker.AllowsNetworkHost(url) {
			stack[0] = api.EncodeI32(-1)
			return
		}
		// Stub: return 0 bytes written (full HTTP client out of scope for v1).
		stack[0] = api.EncodeI32(0)
	}
}

// makeHostReadFile builds the read_file host function with path allowlist enforcement.
// Signature: (pathPtr i32, pathLen i32, outPtr i32, outLen i32) → i32 (bytes written or -1 on deny)
func makeHostReadFile(checker PermissionsChecker) api.GoModuleFunc {
	return func(_ context.Context, mod api.Module, stack []uint64) {
		pathPtr := api.DecodeU32(stack[0])
		pathLen := api.DecodeU32(stack[1])
		mem := mod.Memory()

		pathBytes, ok := mem.Read(pathPtr, pathLen)
		if !ok {
			stack[0] = api.EncodeI32(-1)
			return
		}
		path := string(pathBytes)
		if !checker.AllowsFilePath(path) {
			stack[0] = api.EncodeI32(-1)
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			stack[0] = api.EncodeI32(-1)
			return
		}
		outPtr := api.DecodeU32(stack[2])
		outLen := api.DecodeU32(stack[3])
		n := uint32(len(data))
		if n > outLen {
			n = outLen
		}
		if !mem.Write(outPtr, data[:n]) {
			stack[0] = api.EncodeI32(-1)
			return
		}
		stack[0] = api.EncodeI32(int32(n))
	}
}

// makeHostGetEnv builds the get_env host function with env key allowlist enforcement.
// Signature: (keyPtr i32, keyLen i32, outPtr i32, outLen i32) → i32 (bytes written or -1 on deny)
func makeHostGetEnv(checker PermissionsChecker) api.GoModuleFunc {
	return func(_ context.Context, mod api.Module, stack []uint64) {
		keyPtr := api.DecodeU32(stack[0])
		keyLen := api.DecodeU32(stack[1])
		mem := mod.Memory()

		keyBytes, ok := mem.Read(keyPtr, keyLen)
		if !ok {
			stack[0] = api.EncodeI32(-1)
			return
		}
		key := string(keyBytes)
		if !checker.AllowsEnvKey(key) {
			stack[0] = api.EncodeI32(-1)
			return
		}
		val := os.Getenv(key)
		outPtr := api.DecodeU32(stack[2])
		outLen := api.DecodeU32(stack[3])
		n := uint32(len(val))
		if n > outLen {
			n = outLen
		}
		if !mem.Write(outPtr, []byte(val)[:n]) {
			stack[0] = api.EncodeI32(-1)
			return
		}
		stack[0] = api.EncodeI32(int32(n))
	}
}
