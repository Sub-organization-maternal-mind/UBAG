package cli

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ubag/ubag/apps/gateway/internal/plugins"
)

// defaultPolicyCapabilities lists all capabilities allowed by the default policy.
var defaultPolicyCapabilities = []string{
	string(plugins.CapabilityTransformPrompt),
	string(plugins.CapabilityTransformResponse),
	string(plugins.CapabilityHookJobPre),
	string(plugins.CapabilityHookJobPost),
	string(plugins.CapabilityHookWebhookTransform),
	string(plugins.CapabilityHookValidate),
	string(plugins.CapabilityAdapterExtension),
	string(plugins.CapabilityCommandCustom),
}

// pluginsDir returns the path to ~/.ubag/plugins/.
func pluginsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".ubag", "plugins"), nil
}

// CmdPluginsList lists all installed plugins from ~/.ubag/plugins/.
// It returns a formatted table or "no plugins installed".
func CmdPluginsList() (string, error) {
	dir, err := pluginsDir()
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "no plugins installed", nil
		}
		return "", fmt.Errorf("reading plugins directory: %w", err)
	}

	var rows [][]string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Parse <id>@<version> or just <id> for display.
		id := name
		version := ""
		if idx := strings.LastIndex(name, "@"); idx >= 0 {
			id = name[:idx]
			version = name[idx+1:]
		}
		rows = append(rows, []string{id, version, name})
	}

	if len(rows) == 0 {
		return "no plugins installed", nil
	}

	headers := []string{"ID", "VERSION", "DIRECTORY"}
	return strings.TrimRight(FormatTable(headers, rows), "\n"), nil
}

// CmdPluginsVerify verifies the ed25519 signature of a plugin bundle.
//
// The .sig file must be 96 bytes base64-encoded: 64 bytes raw ed25519 signature
// followed by 32 bytes public key.
//
// The signed payload is SHA256(manifestBytes) || SHA256(wasmBytes).
func CmdPluginsVerify(manifestPath string) (string, error) {
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("reading manifest: %w", err)
	}

	manifest, err := plugins.ParseManifest(manifestBytes)
	if err != nil {
		return "", fmt.Errorf("parsing manifest: %w", err)
	}

	wasmPath := filepath.Join(filepath.Dir(manifestPath), manifest.Entrypoint.Module)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return "", fmt.Errorf("reading wasm module %q: %w", wasmPath, err)
	}

	sigPath := manifestPath + ".sig"
	sigRaw, err := os.ReadFile(sigPath)
	if err != nil {
		return "", fmt.Errorf("reading signature file %q: %w", sigPath, err)
	}

	sig, pubKey, err := decodeSigFile(sigRaw)
	if err != nil {
		return "", err
	}

	payload := buildSignedPayload(manifestBytes, wasmBytes)
	if !ed25519.Verify(pubKey, payload, sig) {
		return "", fmt.Errorf("signature verification failed: signature mismatch")
	}

	return "signature valid", nil
}

// CmdPluginsInstall verifies and installs a plugin from a local path or URL.
//
// policyCapabilities is the allowlist of permitted capabilities.  Pass nil to
// use the default policy (all 8 known capabilities).
func CmdPluginsInstall(source string, policyCapabilities []string) (string, error) {
	if policyCapabilities == nil {
		policyCapabilities = defaultPolicyCapabilities
	}

	// ── 1. Fetch manifest + wasm ─────────────────────────────────────────────
	manifestBytes, wasmBytes, err := fetchBundle(source)
	if err != nil {
		return "", err
	}

	// ── 2. Parse manifest ────────────────────────────────────────────────────
	manifest, err := plugins.ParseManifest(manifestBytes)
	if err != nil {
		return "", fmt.Errorf("parsing manifest: %w", err)
	}

	// ── 3. Verify signature ──────────────────────────────────────────────────
	sigPath := source + ".sig"
	sigRaw, err := os.ReadFile(sigPath)
	if err != nil {
		return "", fmt.Errorf("reading signature file %q: %w", sigPath, err)
	}

	sig, pubKey, err := decodeSigFile(sigRaw)
	if err != nil {
		return "", err
	}

	payload := buildSignedPayload(manifestBytes, wasmBytes)
	if !ed25519.Verify(pubKey, payload, sig) {
		return "", fmt.Errorf("signature verification failed: signature mismatch")
	}

	// ── 4. Policy check ──────────────────────────────────────────────────────
	policySet := make(map[string]struct{}, len(policyCapabilities))
	for _, c := range policyCapabilities {
		policySet[c] = struct{}{}
	}
	for _, cap := range manifest.Capabilities {
		if _, ok := policySet[string(cap)]; !ok {
			return "", fmt.Errorf("capability policy violation: capability %q is not in the policy allowlist", cap)
		}
	}

	// ── 5. Install ───────────────────────────────────────────────────────────
	dir, err := pluginsDir()
	if err != nil {
		return "", err
	}

	pluginDir := filepath.Join(dir, fmt.Sprintf("%s@%s", manifest.ID, manifest.Version))
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		return "", fmt.Errorf("creating plugin directory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), manifestBytes, 0600); err != nil {
		return "", fmt.Errorf("writing manifest: %w", err)
	}

	wasmName := filepath.Base(manifest.Entrypoint.Module)
	if err := os.WriteFile(filepath.Join(pluginDir, wasmName), wasmBytes, 0600); err != nil {
		return "", fmt.Errorf("writing wasm module: %w", err)
	}

	return fmt.Sprintf("installed %s@%s", manifest.ID, manifest.Version), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// fetchBundle reads the manifest and its associated wasm from a local path.
// URL-based sources are not yet supported (planned for a future task).
func fetchBundle(source string) (manifestBytes, wasmBytes []byte, err error) {
	manifestBytes, err = os.ReadFile(source)
	if err != nil {
		return nil, nil, fmt.Errorf("reading manifest %q: %w", source, err)
	}

	// Quick parse just to find the module path — validation happens later.
	manifest, err := plugins.ParseManifest(manifestBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing manifest for wasm path: %w", err)
	}

	wasmPath := filepath.Join(filepath.Dir(source), manifest.Entrypoint.Module)
	wasmBytes, err = os.ReadFile(wasmPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading wasm module %q: %w", wasmPath, err)
	}

	return manifestBytes, wasmBytes, nil
}

// decodeSigFile decodes a base64-encoded .sig file containing a 64-byte
// ed25519 signature followed by a 32-byte public key (96 bytes raw total).
func decodeSigFile(raw []byte) (sig []byte, pubKey ed25519.PublicKey, err error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, nil, fmt.Errorf("decoding signature file: %w", err)
	}
	if len(decoded) != 96 {
		return nil, nil, fmt.Errorf("signature file must be 96 bytes (64 sig + 32 pubkey), got %d", len(decoded))
	}
	sig = decoded[:64]
	pubKey = ed25519.PublicKey(decoded[64:96])
	return sig, pubKey, nil
}

// buildSignedPayload constructs SHA256(manifestBytes) || SHA256(wasmBytes).
func buildSignedPayload(manifestBytes, wasmBytes []byte) []byte {
	mHash := sha256.Sum256(manifestBytes)
	wHash := sha256.Sum256(wasmBytes)
	payload := make([]byte, 64)
	copy(payload[:32], mHash[:])
	copy(payload[32:], wHash[:])
	return payload
}
