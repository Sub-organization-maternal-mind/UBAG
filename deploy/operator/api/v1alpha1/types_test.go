package v1alpha1_test

import (
	"encoding/json"
	"testing"

	. "github.com/ubag/ubag/deploy/operator/api/v1alpha1"
)

func TestTargetDeserialize(t *testing.T) {
	raw := `{"apiVersion":"ubag.dev/v1alpha1","kind":"Target","metadata":{"name":"my-target"},"spec":{"name":"my-target","url":"https://api.example.com","model":"gpt-4"}}`
	var target Target
	if err := json.Unmarshal([]byte(raw), &target); err != nil {
		t.Fatal(err)
	}
	if target.Spec.URL != "https://api.example.com" {
		t.Errorf("URL mismatch: got %q", target.Spec.URL)
	}
	if target.Spec.Model != "gpt-4" {
		t.Errorf("Model mismatch: got %q", target.Spec.Model)
	}
	if target.Spec.Name != "my-target" {
		t.Errorf("Name mismatch: got %q", target.Spec.Name)
	}
}

func TestTargetDeepCopy(t *testing.T) {
	original := &Target{}
	original.Name = "original"
	original.Spec = TargetSpec{
		Name:  "original",
		URL:   "https://example.com",
		Tags:  []string{"a", "b"},
		Model: "gpt-4",
	}
	clone := original.DeepCopy()
	clone.Spec.Tags[0] = "mutated"
	if original.Spec.Tags[0] == "mutated" {
		t.Error("DeepCopy did not isolate Tags slice")
	}
}

func TestAdapterDeserialize(t *testing.T) {
	raw := `{"apiVersion":"ubag.dev/v1alpha1","kind":"Adapter","metadata":{"name":"my-adapter"},"spec":{"name":"my-adapter","type":"openai","config":{"apiKey":"sk-test","region":"us-east-1"}}}`
	var adapter Adapter
	if err := json.Unmarshal([]byte(raw), &adapter); err != nil {
		t.Fatal(err)
	}
	if adapter.Spec.Type != "openai" {
		t.Errorf("Type mismatch: got %q", adapter.Spec.Type)
	}
	if adapter.Spec.Config["apiKey"] != "sk-test" {
		t.Errorf("Config[apiKey] mismatch: got %q", adapter.Spec.Config["apiKey"])
	}
	if adapter.Spec.Config["region"] != "us-east-1" {
		t.Errorf("Config[region] mismatch: got %q", adapter.Spec.Config["region"])
	}
}

func TestAdapterDeepCopy(t *testing.T) {
	original := &Adapter{}
	original.Name = "original"
	original.Spec = AdapterSpec{
		Name:   "original",
		Type:   "openai",
		Config: map[string]string{"key": "value"},
	}
	clone := original.DeepCopy()
	clone.Spec.Config["key"] = "mutated"
	if original.Spec.Config["key"] == "mutated" {
		t.Error("DeepCopy did not isolate Config map")
	}
}

func TestTemplateDeserialize(t *testing.T) {
	raw := `{"apiVersion":"ubag.dev/v1alpha1","kind":"Template","metadata":{"name":"my-template"},"spec":{"name":"my-template","content":"Hello {{name}}!","variables":["name"]}}`
	var tmpl Template
	if err := json.Unmarshal([]byte(raw), &tmpl); err != nil {
		t.Fatal(err)
	}
	if tmpl.Spec.Content != "Hello {{name}}!" {
		t.Errorf("Content mismatch: got %q", tmpl.Spec.Content)
	}
	if len(tmpl.Spec.Variables) != 1 || tmpl.Spec.Variables[0] != "name" {
		t.Errorf("Variables mismatch: got %v", tmpl.Spec.Variables)
	}
}

func TestTemplateDeepCopy(t *testing.T) {
	original := &Template{}
	original.Name = "original"
	original.Spec = TemplateSpec{
		Name:      "original",
		Content:   "Hello {{name}}!",
		Variables: []string{"name"},
	}
	clone := original.DeepCopy()
	clone.Spec.Variables[0] = "mutated"
	if original.Spec.Variables[0] == "mutated" {
		t.Error("DeepCopy did not isolate Variables slice")
	}
}

func TestAppDeserialize(t *testing.T) {
	raw := `{"apiVersion":"ubag.dev/v1alpha1","kind":"App","metadata":{"name":"my-app"},"spec":{"name":"my-app","description":"Test app","targets":["target-a","target-b"]}}`
	var app App
	if err := json.Unmarshal([]byte(raw), &app); err != nil {
		t.Fatal(err)
	}
	if app.Spec.Description != "Test app" {
		t.Errorf("Description mismatch: got %q", app.Spec.Description)
	}
	if len(app.Spec.Targets) != 2 {
		t.Errorf("Targets length mismatch: got %d", len(app.Spec.Targets))
	}
	if app.Spec.Targets[0] != "target-a" {
		t.Errorf("Targets[0] mismatch: got %q", app.Spec.Targets[0])
	}
}

func TestAppDeepCopy(t *testing.T) {
	original := &App{}
	original.Name = "original"
	original.Spec = AppSpec{
		Name:        "original",
		Description: "Test",
		Targets:     []string{"t1", "t2"},
	}
	clone := original.DeepCopy()
	clone.Spec.Targets[0] = "mutated"
	if original.Spec.Targets[0] == "mutated" {
		t.Error("DeepCopy did not isolate Targets slice")
	}
}

func TestDeepCopyObject(t *testing.T) {
	target := &Target{}
	target.Name = "test"
	obj := target.DeepCopyObject()
	if obj == nil {
		t.Fatal("DeepCopyObject returned nil")
	}
	if _, ok := obj.(*Target); !ok {
		t.Errorf("DeepCopyObject returned wrong type: %T", obj)
	}

	adapter := &Adapter{}
	if obj := adapter.DeepCopyObject(); obj == nil {
		t.Error("Adapter.DeepCopyObject returned nil")
	}

	tmpl := &Template{}
	if obj := tmpl.DeepCopyObject(); obj == nil {
		t.Error("Template.DeepCopyObject returned nil")
	}

	app := &App{}
	if obj := app.DeepCopyObject(); obj == nil {
		t.Error("App.DeepCopyObject returned nil")
	}
}

func TestResourceStatus(t *testing.T) {
	raw := `{"apiVersion":"ubag.dev/v1alpha1","kind":"Target","metadata":{"name":"t"},"spec":{"name":"t","url":"https://x.com"},"status":{"observedGeneration":3,"ready":true,"lastSyncedHash":"abc123"}}`
	var target Target
	if err := json.Unmarshal([]byte(raw), &target); err != nil {
		t.Fatal(err)
	}
	if target.Status.ObservedGeneration != 3 {
		t.Errorf("ObservedGeneration mismatch: got %d", target.Status.ObservedGeneration)
	}
	if !target.Status.Ready {
		t.Error("Ready should be true")
	}
	if target.Status.LastSyncedHash != "abc123" {
		t.Errorf("LastSyncedHash mismatch: got %q", target.Status.LastSyncedHash)
	}
}
