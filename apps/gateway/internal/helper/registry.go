package helper

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxRegistryFileBytes = 4 << 20
	maxRegistryAdapters  = 128
)

// RegistryDigest is the canonical SHA-256 (lowercase hex) of an adapter
// registry directory: registry.json plus every manifest it lists, each parsed
// and re-encoded canonically (sorted keys, no insignificant whitespace, numbers
// untouched) so line endings and formatting never change it, and the manifests
// taken in id order. It covers the manifest set (selectors, postures, artifact
// policy), not worker code; WorkloadVersion covers the code.
//
// Both sides compute it with this function: the helper reports it in Handshake
// and every capacity report, and the primary compares it with its own
// adapters/ directory. An unreadable or malformed registry is an error, never a
// default digest (a helper that cannot load its registry must not run attempts).
func RegistryDigest(dir string) (string, error) {
	raw, err := readCapped(filepath.Join(dir, "registry.json"))
	if err != nil {
		return "", err
	}
	var reg struct {
		Adapters []registryEntry `json:"adapters"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		return "", fmt.Errorf("registry.json: %w", err)
	}
	if len(reg.Adapters) == 0 || len(reg.Adapters) > maxRegistryAdapters {
		return "", errors.New("registry.json: no adapters, or too many")
	}
	canon, err := canonicalJSON(raw)
	if err != nil {
		return "", fmt.Errorf("registry.json: %w", err)
	}
	h := sha256.New()
	h.Write([]byte("ubag.registry-digest.v1\n"))
	h.Write(canon)
	adapters := reg.Adapters
	slices.SortFunc(adapters, func(a, b registryEntry) int { return strings.Compare(a.ID, b.ID) })
	for i, a := range adapters {
		if a.ID == "" || !filepath.IsLocal(a.Manifest) || (i > 0 && adapters[i-1].ID == a.ID) {
			return "", fmt.Errorf("registry.json: adapter %q has no id, a non-local manifest path, or a duplicate id", a.ID)
		}
		mraw, err := readCapped(filepath.Join(dir, filepath.FromSlash(a.Manifest)))
		if err != nil {
			return "", err
		}
		mcanon, err := canonicalJSON(mraw)
		if err != nil {
			return "", fmt.Errorf("manifest of %q: %w", a.ID, err)
		}
		h.Write([]byte("\n" + a.ID + "\n"))
		h.Write(mcanon)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type registryEntry struct {
	ID       string `json:"id"`
	Manifest string `json:"manifest"`
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxRegistryFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxRegistryFileBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), maxRegistryFileBytes)
	}
	return b, nil
}

// canonicalJSON re-encodes a JSON document with sorted object keys and the
// numbers exactly as written (json.Number).
func canonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON document")
	}
	return json.Marshal(v)
}
