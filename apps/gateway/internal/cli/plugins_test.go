package cli_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// testWasmPath is the echo_transform.wasm from the plugins testdata directory.
const testWasmPath = "../../internal/plugins/testdata/echo_transform.wasm"

// validPluginManifest returns a JSON-encoded manifest that uses the given wasm
// module filename.
func validPluginManifest(t *testing.T, wasmName string) []byte {
	t.Helper()
	m := map[string]any{
		"schema_version": "ubag.plugin.v0",
		"id":             "echo-transform",
		"display_name":   "Echo Transform",
		"version":        "1.0.0",
		"description":    "Test plugin",
		"capabilities":   []any{"transform.prompt"},
		"entrypoint": map[string]any{
			"type":   "wasi-component",
			"module": wasmName,
			"exports": map[string]any{
				"transform": "run",
			},
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
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return data
}

// buildSigFile encodes [signature || publicKey] as base64 (96 bytes decoded).
func buildSigFile(sig, pubKey []byte) []byte {
	raw := append(sig, pubKey...)
	encoded := base64.StdEncoding.EncodeToString(raw)
	return []byte(encoded)
}

// signPayload signs SHA256(manifestBytes) || SHA256(wasmBytes) with privKey.
func signPayload(t *testing.T, privKey ed25519.PrivateKey, manifestBytes, wasmBytes []byte) []byte {
	t.Helper()
	mHash := sha256.Sum256(manifestBytes)
	wHash := sha256.Sum256(wasmBytes)
	payload := make([]byte, 64)
	copy(payload[:32], mHash[:])
	copy(payload[32:], wHash[:])
	return ed25519.Sign(privKey, payload)
}

// writeBundle writes manifest, wasm, and .sig to dir. Returns the manifest path.
func writeBundle(t *testing.T, dir string, manifestBytes, wasmBytes, sigFileBytes []byte, wasmName string) string {
	t.Helper()
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, wasmName), wasmBytes, 0600); err != nil {
		t.Fatalf("write wasm: %v", err)
	}
	if err := os.WriteFile(manifestPath+".sig", sigFileBytes, 0600); err != nil {
		t.Fatalf("write sig: %v", err)
	}
	return manifestPath
}

// setHomeDir overrides HOME (Unix) / USERPROFILE+HOMEDRIVE+HOMEPATH (Windows)
// for the duration of the test so that os.UserHomeDir() returns dir.
func setHomeDir(t *testing.T, dir string) {
	t.Helper()
	// os.UserHomeDir on Windows uses USERPROFILE first.
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestPluginsInstall_SignatureMismatch verifies that a bundle with a wrong
// signature is rejected with an error containing "signature".
func TestPluginsInstall_SignatureMismatch(t *testing.T) {
	wasmBytes, err := os.ReadFile(testWasmPath)
	if err != nil {
		t.Fatalf("reading test wasm: %v", err)
	}
	const wasmName = "echo_transform.wasm"
	manifestBytes := validPluginManifest(t, wasmName)

	// Generate a key pair and sign, then corrupt the signature.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig := signPayload(t, privKey, manifestBytes, wasmBytes)
	// Corrupt: flip first byte of signature.
	sig[0] ^= 0xFF

	sigFile := buildSigFile(sig, pubKey)

	dir := t.TempDir()
	manifestPath := writeBundle(t, dir, manifestBytes, wasmBytes, sigFile, wasmName)

	homeDir := t.TempDir()
	setHomeDir(t, homeDir)

	_, err = cli.CmdPluginsInstall(manifestPath, nil)
	if err == nil {
		t.Fatal("expected error from signature mismatch, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "signature") {
		t.Errorf("error should mention 'signature', got: %v", err)
	}
}

// TestPluginsInstall_CapabilityPolicyViolation verifies that a bundle whose
// manifest requests a capability not in the policy allowlist is rejected with
// an error containing "capability" or "policy".
func TestPluginsInstall_CapabilityPolicyViolation(t *testing.T) {
	wasmBytes, err := os.ReadFile(testWasmPath)
	if err != nil {
		t.Fatalf("reading test wasm: %v", err)
	}
	const wasmName = "echo_transform.wasm"
	// Manifest requests "transform.prompt".
	manifestBytes := validPluginManifest(t, wasmName)

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig := signPayload(t, privKey, manifestBytes, wasmBytes)
	sigFile := buildSigFile(sig, pubKey)

	dir := t.TempDir()
	manifestPath := writeBundle(t, dir, manifestBytes, wasmBytes, sigFile, wasmName)

	homeDir := t.TempDir()
	setHomeDir(t, homeDir)

	// Policy that does NOT include "transform.prompt".
	restrictedPolicy := []string{
		"hook.job.pre",
		"hook.job.post",
	}

	_, err = cli.CmdPluginsInstall(manifestPath, restrictedPolicy)
	if err == nil {
		t.Fatal("expected error from capability policy violation, got nil")
	}
	errLower := strings.ToLower(err.Error())
	if !strings.Contains(errLower, "capability") && !strings.Contains(errLower, "policy") {
		t.Errorf("error should mention 'capability' or 'policy', got: %v", err)
	}
}

// TestPluginsInstall_ValidBundleInstallsToRightPath verifies that a valid
// bundle is installed into <homeDir>/.ubag/plugins/<id>@<version>/ with both
// manifest.json and the wasm file present.
func TestPluginsInstall_ValidBundleInstallsToRightPath(t *testing.T) {
	wasmBytes, err := os.ReadFile(testWasmPath)
	if err != nil {
		t.Fatalf("reading test wasm: %v", err)
	}
	const wasmName = "echo_transform.wasm"
	manifestBytes := validPluginManifest(t, wasmName)

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig := signPayload(t, privKey, manifestBytes, wasmBytes)
	sigFile := buildSigFile(sig, pubKey)

	dir := t.TempDir()
	manifestPath := writeBundle(t, dir, manifestBytes, wasmBytes, sigFile, wasmName)

	homeDir := t.TempDir()
	setHomeDir(t, homeDir)

	out, err := cli.CmdPluginsInstall(manifestPath, nil)
	if err != nil {
		t.Fatalf("CmdPluginsInstall() error: %v", err)
	}
	if !strings.Contains(out, "echo-transform") {
		t.Errorf("output should contain plugin id 'echo-transform', got: %q", out)
	}

	// Verify the install directory exists and contains the expected files.
	pluginDir := filepath.Join(homeDir, ".ubag", "plugins", "echo-transform@1.0.0")
	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		t.Fatalf("expected plugin directory %q to exist", pluginDir)
	}

	expectedFiles := []string{"manifest.json", wasmName}
	for _, f := range expectedFiles {
		path := filepath.Join(pluginDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %q to exist in plugin directory", path)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdPluginsList
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdPluginsList_Empty(t *testing.T) {
	homeDir := t.TempDir()
	setHomeDir(t, homeDir)

	out, err := cli.CmdPluginsList()
	if err != nil {
		t.Fatalf("CmdPluginsList() error: %v", err)
	}
	if !strings.Contains(out, "no plugins installed") {
		t.Errorf("expected 'no plugins installed', got: %q", out)
	}
}

func TestCmdPluginsList_WithPlugins(t *testing.T) {
	homeDir := t.TempDir()
	setHomeDir(t, homeDir)

	// Create a fake plugin directory.
	pluginDir := filepath.Join(homeDir, ".ubag", "plugins", "my-plugin@1.0.0")
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	out, err := cli.CmdPluginsList()
	if err != nil {
		t.Fatalf("CmdPluginsList() error: %v", err)
	}
	if !strings.Contains(out, "my-plugin") {
		t.Errorf("expected 'my-plugin' in output: %q", out)
	}
	if !strings.Contains(out, "1.0.0") {
		t.Errorf("expected '1.0.0' in output: %q", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdPluginsVerify
// ─────────────────────────────────────────────────────────────────────────────

func TestCmdPluginsVerify_ValidSignature(t *testing.T) {
	wasmBytes, err := os.ReadFile(testWasmPath)
	if err != nil {
		t.Fatalf("reading test wasm: %v", err)
	}
	const wasmName = "echo_transform.wasm"
	manifestBytes := validPluginManifest(t, wasmName)

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig := signPayload(t, privKey, manifestBytes, wasmBytes)
	sigFile := buildSigFile(sig, pubKey)

	dir := t.TempDir()
	manifestPath := writeBundle(t, dir, manifestBytes, wasmBytes, sigFile, wasmName)

	out, err := cli.CmdPluginsVerify(manifestPath)
	if err != nil {
		t.Fatalf("CmdPluginsVerify() error: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "valid") {
		t.Errorf("expected 'valid' in output, got: %q", out)
	}
}

func TestCmdPluginsVerify_InvalidSignature(t *testing.T) {
	wasmBytes, err := os.ReadFile(testWasmPath)
	if err != nil {
		t.Fatalf("reading test wasm: %v", err)
	}
	const wasmName = "echo_transform.wasm"
	manifestBytes := validPluginManifest(t, wasmName)

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig := signPayload(t, privKey, manifestBytes, wasmBytes)
	// Corrupt the signature.
	sig[0] ^= 0xFF
	sigFile := buildSigFile(sig, pubKey)

	dir := t.TempDir()
	manifestPath := writeBundle(t, dir, manifestBytes, wasmBytes, sigFile, wasmName)

	_, err = cli.CmdPluginsVerify(manifestPath)
	if err == nil {
		t.Fatal("expected error from invalid signature, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "signature") {
		t.Errorf("error should mention 'signature', got: %v", err)
	}
}
