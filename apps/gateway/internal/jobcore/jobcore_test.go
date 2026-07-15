package jobcore

import (
	"strings"
	"testing"
)

func mockCatalog() ModelCatalog {
	return ModelCatalog{Settings: map[string]CatalogSetting{
		"model":     {Kind: "choice", Values: []string{"mock-fast", "mock-deep"}},
		"thinking":  {Kind: "choice", Values: []string{"standard", "extended"}},
		"deepthink": {Kind: "toggle"},
	}}
}

func TestValidateModelSettingsAcceptsCatalogValues(t *testing.T) {
	settings := map[string]any{"model": "mock-deep", "thinking": "extended", "deepthink": true}
	if err := ValidateModelSettings("mock", settings, mockCatalog()); err != nil {
		t.Fatalf("want nil for in-catalog settings, got %v", err)
	}
}

func TestValidateModelSettingsRejectsUnknownModelValue(t *testing.T) {
	err := ValidateModelSettings("mock", map[string]any{"model": "gpt-nonexistent"}, mockCatalog())
	if err == nil {
		t.Fatal("want error for out-of-catalog model, got nil")
	}
	if !strings.Contains(err.Error(), "UBAG-VALIDATION-MODEL-UNAVAILABLE-001") {
		t.Fatalf("error = %v, want UBAG-VALIDATION-MODEL-UNAVAILABLE-001", err)
	}
}

func TestValidateModelSettingsRejectsUnknownSettingKey(t *testing.T) {
	err := ValidateModelSettings("mock", map[string]any{"nope": "x"}, mockCatalog())
	if err == nil || !strings.Contains(err.Error(), "UBAG-VALIDATION-MODE-UNAVAILABLE-001") {
		t.Fatalf("error = %v, want UBAG-VALIDATION-MODE-UNAVAILABLE-001", err)
	}
}

func TestValidateModelSettingsRejectsBadThinkingValue(t *testing.T) {
	err := ValidateModelSettings("mock", map[string]any{"thinking": "ludicrous"}, mockCatalog())
	if err == nil || !strings.Contains(err.Error(), "UBAG-VALIDATION-MODE-UNAVAILABLE-001") {
		t.Fatalf("error = %v, want UBAG-VALIDATION-MODE-UNAVAILABLE-001", err)
	}
}

func TestValidateModelSettingsRejectsStringForToggle(t *testing.T) {
	// deepthink is kind=toggle: the worker passes the value to a boolean
	// comparison, so a string here would silently mean "truthy".
	err := ValidateModelSettings("mock", map[string]any{"deepthink": "yes"}, mockCatalog())
	if err == nil || !strings.Contains(err.Error(), "UBAG-VALIDATION-MODE-UNAVAILABLE-001") {
		t.Fatalf("error = %v, want UBAG-VALIDATION-MODE-UNAVAILABLE-001", err)
	}
}

func TestValidateModelSettingsRejectsBoolForChoice(t *testing.T) {
	err := ValidateModelSettings("mock", map[string]any{"model": true}, mockCatalog())
	if err == nil {
		t.Fatal("want error for boolean value on a choice setting, got nil")
	}
}

func TestValidateModelSettingsEmptyCatalogRejectsAnySetting(t *testing.T) {
	// chatgpt_web ships an empty catalog: nothing is caller-selectable, so a
	// request that thinks it is picking a model must fail loudly rather than
	// be silently ignored.
	err := ValidateModelSettings("chatgpt_web", map[string]any{"model": "anything"}, ModelCatalog{})
	if err == nil {
		t.Fatal("want error when the catalog is empty, got nil")
	}
}

func TestValidateModelSettingsNilIsAllowed(t *testing.T) {
	// Omitted model_settings must keep today's operator defaults.
	if err := ValidateModelSettings("mock", nil, ModelCatalog{}); err != nil {
		t.Fatalf("want nil for absent settings, got %v", err)
	}
}

func TestValidateModelSettingsRejectsReservedKey(t *testing.T) {
	// _enabled / _new_chat are reserved worker control keys. The schema pattern
	// blocks them at the edge; this is defense in depth for gRPC/batch paths.
	err := ValidateModelSettings("mock", map[string]any{"_enabled": "false"}, mockCatalog())
	if err == nil {
		t.Fatal("want error for reserved _-prefixed key, got nil")
	}
}
