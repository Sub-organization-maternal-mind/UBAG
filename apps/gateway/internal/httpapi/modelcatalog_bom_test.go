package httpapi

import "testing"

// Regression: encoding/json rejects a UTF-8 BOM, so a BOM-prefixed adapter
// manifest used to decode as an empty model catalog and fail-close every
// supplied model setting.
func TestDecodeModelCatalogAcceptsUTF8BOM(t *testing.T) {
	raw := append(
		[]byte{0xEF, 0xBB, 0xBF},
		[]byte(`{"model_catalog":{"settings":{"model":{"kind":"choice","values":["GPT-5.6 Sol"]}}}}`)...,
	)

	catalog, err := decodeModelCatalog(raw)
	if err != nil {
		t.Fatalf("decode BOM-prefixed manifest: %v", err)
	}
	setting, ok := catalog.Settings["model"]
	if len(catalog.Settings) != 1 || !ok || len(setting.Values) != 1 || setting.Values[0] != "GPT-5.6 Sol" {
		t.Fatalf("unexpected model catalog: %#v", catalog)
	}
}
