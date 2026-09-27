package templates

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func testTemplate(id string) Template {
	timestamp := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	return Template{
		ID:          id,
		TenantID:    "*",
		AppID:       "*",
		Name:        "test " + id,
		Target:      "mock_target",
		CommandType: "echo",
		Body:        "Hello {{ name }}",
		CreatedAt:   timestamp,
		UpdatedAt:   timestamp,
	}
}

// --- Scoping and lookup -----------------------------------------------------

func TestNewMemoryStoreDefaultsToCatalog(t *testing.T) {
	store := NewMemoryStore()
	items, err := store.List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].ID != "mock.echo.v1" {
		t.Fatalf("expected default catalog with mock.echo.v1, got %+v", items)
	}
}

func TestGetScopedVisibility(t *testing.T) {
	store := NewMemoryStore(Template{ID: "t.global"}, Template{ID: "t.tenant", TenantID: "tenant_a"}, Template{ID: "t.app", AppID: "app_a"})
	cases := []struct {
		id       string
		tenantID string
		appID    string
		want     bool
	}{
		// TenantID/AppID "*" (and empty) are visible to every scope.
		{"t.global", "tenant_a", "app_a", true},
		{"t.global", "tenant_b", "app_b", true},
		// Exact tenant binding only matches that tenant.
		{"t.tenant", "tenant_a", "app_a", true},
		{"t.tenant", "tenant_b", "app_a", false},
		// Exact app binding only matches that app.
		{"t.app", "tenant_a", "app_a", true},
		{"t.app", "tenant_a", "app_b", false},
		// Unknown ids never match.
		{"t.missing", "tenant_a", "app_a", false},
	}
	for _, tc := range cases {
		if _, ok, _ := store.GetScoped(context.Background(), tc.id, tc.tenantID, tc.appID); ok != tc.want {
			t.Errorf("GetScoped(%q, %q, %q) ok = %v, want %v", tc.id, tc.tenantID, tc.appID, ok, tc.want)
		}
	}
}

// The lookup normalizes the id with strings.TrimSpace, so padded ids from
// route parameters resolve to the same template.
func TestGetScopedTrimsID(t *testing.T) {
	store := NewMemoryStore(testTemplate("mock.echo.v1"))
	if _, ok, _ := store.GetScoped(context.Background(), "  mock.echo.v1  ", "*", "*"); !ok {
		t.Fatal("padded id should resolve after TrimSpace normalization")
	}
}

func TestListFilterCursorAndLimit(t *testing.T) {
	store := NewMemoryStore(
		testTemplate("a.first"),
		testTemplate("b.second"),
		testTemplate("c.third"),
	)
	list := func(filter ListFilter) []string {
		items, err := store.List(context.Background(), filter)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		return ids
	}

	// Results are ordered by ID ascending regardless of insertion order.
	got := list(ListFilter{})
	if strings.Join(got, ",") != "a.first,b.second,c.third" {
		t.Fatalf("expected ID-ordered page, got %v", got)
	}
	// AfterID is an exclusive cursor on the ID ordering.
	got = list(ListFilter{AfterID: "a.first"})
	if strings.Join(got, ",") != "b.second,c.third" {
		t.Fatalf("AfterID should page after the cursor, got %v", got)
	}
	// An unknown AfterID is ignored (full window).
	got = list(ListFilter{AfterID: "zzz.unknown"})
	if len(got) != 3 {
		t.Fatalf("unknown AfterID should be ignored, got %v", got)
	}
	// Limit bounds the page size; 0 means no limit.
	got = list(ListFilter{Limit: 2})
	if len(got) != 2 || got[1] != "b.second" {
		t.Fatalf("Limit should truncate the ordered page, got %v", got)
	}
}

// Set must clone the stored template (and its default maps) so later caller
// mutations cannot rewrite store contents.
func TestSetClonesInput(t *testing.T) {
	store := NewMemoryStore()
	tmpl := testTemplate("x.cloned")
	tmpl.InputDefaults = map[string]any{"prompt": "original"}
	store.Set(tmpl)

	tmpl.InputDefaults["prompt"] = "mutated"
	tmpl.Body = "mutated body"

	got, ok, _ := store.GetScoped(context.Background(), "x.cloned", "*", "*")
	if !ok {
		t.Fatal("Set template should be retrievable")
	}
	if got.Body != "Hello {{ name }}" {
		t.Fatalf("store returned mutated Body: %q", got.Body)
	}
	if got.InputDefaults["prompt"] != "original" {
		t.Fatalf("store returned mutated InputDefaults: %v", got.InputDefaults)
	}

	// Mutating the returned copy must not leak into the store either.
	got.InputDefaults["prompt"] = "mutated-again"
	again, _, _ := store.GetScoped(context.Background(), "x.cloned", "*", "*")
	if again.InputDefaults["prompt"] != "original" {
		t.Fatalf("returned template shares state with the store: %v", again.InputDefaults)
	}
}

// --- Render ------------------------------------------------------------------

func TestRenderValidTemplate(t *testing.T) {
	store := NewMemoryStore(testTemplate("render.ok"))
	out, err := store.Render(context.Background(), "render.ok", "tenant_a", "app_a", map[string]any{"name": "UBAG"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "Hello UBAG" {
		t.Fatalf("Render = %q, want %q", out, "Hello UBAG")
	}
}

// An empty Body is backward-compatible: Render returns an empty string
// without error (see the Template.Body contract).
func TestRenderEmptyBody(t *testing.T) {
	tmpl := testTemplate("render.empty")
	tmpl.Body = ""
	store := NewMemoryStore(tmpl)
	out, err := store.Render(context.Background(), "render.empty", "*", "*", nil)
	if err != nil {
		t.Fatalf("Render with empty body: %v", err)
	}
	if out != "" {
		t.Fatalf("Render = %q, want empty", out)
	}
}

// Missing variables render as empty substitutions, never errors: Pongo2
// resolves unknown identifiers to the empty value.
func TestRenderMissingVariable(t *testing.T) {
	store := NewMemoryStore(testTemplate("render.missing-var"))
	out, err := store.Render(context.Background(), "render.missing-var", "*", "*", nil)
	if err != nil {
		t.Fatalf("missing variable must not error: %v", err)
	}
	if out != "Hello " {
		t.Fatalf("missing variable should substitute empty, got %q", out)
	}
}

// Unknown template ids fail with ErrNotFound (wrapped, so callers can
// errors.Is against it).
func TestRenderNotFoundWrapsErrNotFound(t *testing.T) {
	store := NewMemoryStore()
	_, err := store.Render(context.Background(), "render.absent", "*", "*", nil)
	if err == nil {
		t.Fatal("expected ErrNotFound for unknown template")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error should wrap ErrNotFound, got: %v", err)
	}
	// Scoping is enforced on the Render path too: the template exists but is
	// invisible to this tenant, which is the same ErrNotFound outcome.
	tenantTemplate := testTemplate("render.scoped")
	tenantTemplate.TenantID = "tenant_a"
	store = NewMemoryStore(tenantTemplate)
	if _, err := store.Render(context.Background(), "render.scoped", "tenant_b", "*", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope render should be ErrNotFound, got: %v", err)
	}
}

// Malformed template bodies are rejected at compile time with an error that
// names the offending template, never a panic.
func TestRenderMalformedTemplate(t *testing.T) {
	tmpl := testTemplate("render.malformed")
	tmpl.Body = "{% if name %}unclosed"
	store := NewMemoryStore(tmpl)
	_, err := store.Render(context.Background(), "render.malformed", "*", "*", nil)
	if err == nil {
		t.Fatal("malformed template body must be rejected")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("compile failure must not be misreported as ErrNotFound: %v", err)
	}
	if !strings.Contains(err.Error(), "render.malformed") {
		t.Fatalf("compile error should name the template, got: %v", err)
	}
	if !strings.Contains(err.Error(), "compile") {
		t.Fatalf("expected a pongo2 compile error, got: %v", err)
	}
}

// Variable output is HTML-escaped by Pongo2's default autoescape, so untrusted
// values rendered through a template body cannot inject raw markup into the
// rendered prompt.
func TestRenderEscapesVariableOutput(t *testing.T) {
	tmpl := testTemplate("render.escape")
	tmpl.Body = "{{ payload }}"
	store := NewMemoryStore(tmpl)
	out, err := store.Render(context.Background(), "render.escape", "*", "*", map[string]any{
		"payload": "<script>&\"quote\"",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if want := "&lt;script&gt;&amp;&quot;quote&quot;"; out != want {
		t.Fatalf("render output should be HTML-escaped, got %q want %q", out, want)
	}
}
