package plugins_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/plugins"
)

// loadWASM reads a fixture from testdata/.
func loadWASM(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("loadWASM %s: %v", name, err)
	}
	return b
}

// minimalManifest builds a valid Manifest with sensible defaults for tests.
func minimalManifest(hostFns []plugins.HostFunctionName, maxMemBytes, maxExecMS int64) plugins.Manifest {
	return plugins.Manifest{
		SchemaVersion: plugins.SchemaVersion,
		ID:            "test-plugin",
		DisplayName:   "Test Plugin",
		Version:       "0.1.0",
		Capabilities:  []plugins.Capability{plugins.CapabilityTransformPrompt},
		Entrypoint: plugins.Entrypoint{
			Type:   plugins.EntrypointCoreModule,
			Module: "test.wasm",
		},
		Engine: plugins.Engine{Runtime: plugins.RuntimeCore},
		Permissions: plugins.Permissions{
			HostFunctions:  hostFns,
			MaxMemoryBytes: maxMemBytes,
			MaxExecutionMS: maxExecMS,
		},
	}
}

// TestTransformRoundTrip verifies that the echo_transform module echoes input.
func TestWasmTransformRoundTrip(t *testing.T) {
	wasmBytes := loadWASM(t, "echo_transform.wasm")
	manifest := minimalManifest(nil, 1*1024*1024, 1000)

	exec, err := plugins.NewWasmExecutor(context.Background(), "echo-plugin", manifest, wasmBytes)
	if err != nil {
		t.Fatalf("NewWasmExecutor: %v", err)
	}
	defer exec.Close(context.Background())

	input := []byte(`{"hello":"world"}`)
	got, err := exec.Transform(context.Background(), input)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if string(got) != string(input) {
		t.Fatalf("echo mismatch: got %q, want %q", got, input)
	}
}

// TestWasmTimeout verifies that a non-terminating transform is killed by deadline.
func TestWasmTimeout(t *testing.T) {
	wasmBytes := loadWASM(t, "infinite_loop.wasm")
	// 50 ms execution limit
	manifest := minimalManifest(nil, 1*1024*1024, 50)

	exec, err := plugins.NewWasmExecutor(context.Background(), "loop-plugin", manifest, wasmBytes)
	if err != nil {
		t.Fatalf("NewWasmExecutor: %v", err)
	}
	defer exec.Close(context.Background())

	start := time.Now()
	_, err = exec.Transform(context.Background(), []byte(`{}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	// Error must mention deadline or timeout.
	errStr := strings.ToLower(err.Error())
	if !strings.Contains(errStr, "deadline") && !strings.Contains(errStr, "timeout") && !strings.Contains(errStr, "context") {
		t.Fatalf("expected deadline/timeout error, got: %v", err)
	}

	// Should complete well under 2 s.
	if elapsed > 2*time.Second {
		t.Fatalf("transform took too long: %v", elapsed)
	}
}

// TestWasmDeniedImportTraps verifies that loading a module with an unlisted import fails.
func TestWasmDeniedImportTraps(t *testing.T) {
	wasmBytes := loadWASM(t, "import_denied.wasm")
	// Manifest deliberately does NOT include "log" in host_functions.
	manifest := minimalManifest(nil, 1*1024*1024, 1000)

	_, err := plugins.NewWasmExecutor(context.Background(), "denied-plugin", manifest, wasmBytes)
	if err == nil {
		t.Fatal("expected instantiation error for denied import, got nil")
	}
}

// TestWasmMemoryLimit verifies that a module requesting more than the allowed pages
// fails instantiation when the runtime limit is 1 page (65536 bytes).
func TestWasmMemoryLimit(t *testing.T) {
	// We need a WASM module whose memory section declares more than 1 page.
	// Minimum pages = 2 (131072 bytes). We inline the binary here.
	//
	// (module
	//   (memory 2)   ;; requests 2 pages minimum
	//   (func (export "alloc") (param i32) (result i32) i32.const 0)
	//   (func (export "transform") (param i32 i32) (result i64) i64.const 0)
	// )
	twoPageWASM := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		// type section
		0x01, 0x0c, 0x02,
		0x60, 0x01, 0x7f, 0x01, 0x7f,
		0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7e,
		// function section
		0x03, 0x03, 0x02, 0x00, 0x01,
		// memory section: (memory 2) — min=2, no max
		0x05, 0x03, 0x01, 0x00, 0x02,
		// export section
		0x07, 0x1e, 0x03,
		0x06, 0x6d, 0x65, 0x6d, 0x6f, 0x72, 0x79, 0x02, 0x00,
		0x05, 0x61, 0x6c, 0x6c, 0x6f, 0x63, 0x00, 0x00,
		0x09, 0x74, 0x72, 0x61, 0x6e, 0x73, 0x66, 0x6f, 0x72, 0x6d, 0x00, 0x01,
		// code section
		0x0a, 0x09, 0x02,
		0x04, 0x00, 0x41, 0x00, 0x0b,
		0x04, 0x00, 0x42, 0x00, 0x0b,
	}

	// Allow only 1 page = 65536 bytes.
	manifest := minimalManifest(nil, 65536, 1000)

	_, err := plugins.NewWasmExecutor(context.Background(), "memlimit-plugin", manifest, twoPageWASM)
	if err == nil {
		t.Fatal("expected memory-limit error, got nil")
	}
}
