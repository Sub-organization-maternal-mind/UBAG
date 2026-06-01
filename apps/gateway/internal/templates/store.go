package templates

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flosch/pongo2/v6"
)

var ErrNotFound = errors.New("template not found")

type Template struct {
	ID              string
	TenantID        string
	AppID           string
	Name            string
	Description     string
	Target          string
	CommandType     string
	// Body is the Pongo2/Jinja2-compatible template source. When empty, Render
	// returns an empty string without error (backward-compatible).
	Body            string
	InputDefaults   map[string]any
	OptionsDefaults map[string]any
	Sensitive       bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ListFilter struct {
	TenantID string
	AppID    string
}

type Store interface {
	Ready(ctx context.Context) error
	List(ctx context.Context, filter ListFilter) ([]Template, error)
	GetScoped(ctx context.Context, id string, tenantID string, appID string) (Template, bool, error)
}

// RenderStore extends Store with Pongo2 template rendering (§17).
type RenderStore interface {
	Store
	// Render retrieves template id scoped to tenantID+appID, executes it with
	// vars using Pongo2, and returns the rendered string. Returns ErrNotFound
	// when the template does not exist.
	Render(ctx context.Context, id, tenantID, appID string, vars map[string]any) (string, error)
}

type MemoryStore struct {
	items []Template
}

func NewMemoryStore(items ...Template) *MemoryStore {
	if len(items) == 0 {
		items = DefaultCatalog()
	}
	copied := make([]Template, 0, len(items))
	for _, item := range items {
		copied = append(copied, cloneTemplate(item))
	}
	sortTemplates(copied)
	return &MemoryStore{items: copied}
}

func DefaultCatalog() []Template {
	timestamp := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	return []Template{
		{
			ID:          "mock.echo.v1",
			TenantID:    "*",
			AppID:       "*",
			Name:        "Mock echo",
			Description: "Safe built-in template for deterministic local mock-worker echo jobs.",
			Target:      "mock_target",
			CommandType: "echo",
			InputDefaults: map[string]any{
				"prompt": "Hello UBAG",
			},
			OptionsDefaults: map[string]any{
				"return_mode":  "final",
				"cache_policy": "none",
			},
			CreatedAt: timestamp,
			UpdatedAt: timestamp,
		},
	}
}

func (s *MemoryStore) Ready(context.Context) error {
	return nil
}

func (s *MemoryStore) List(_ context.Context, filter ListFilter) ([]Template, error) {
	result := []Template{}
	for _, item := range s.items {
		if templateVisibleToScope(item, filter.TenantID, filter.AppID) {
			result = append(result, cloneTemplate(item))
		}
	}
	sortTemplates(result)
	return result, nil
}

func (s *MemoryStore) GetScoped(_ context.Context, id string, tenantID string, appID string) (Template, bool, error) {
	id = strings.TrimSpace(id)
	for _, item := range s.items {
		if item.ID == id && templateVisibleToScope(item, tenantID, appID) {
			return cloneTemplate(item), true, nil
		}
	}
	return Template{}, false, nil
}

// Set adds or replaces a template in the store (used in tests and admin flows).
func (s *MemoryStore) Set(tmpl Template) {
	for i, item := range s.items {
		if item.ID == tmpl.ID {
			s.items[i] = cloneTemplate(tmpl)
			return
		}
	}
	s.items = append(s.items, cloneTemplate(tmpl))
	sortTemplates(s.items)
}

// Render retrieves template id, compiles it with Pongo2, and executes it with vars.
func (s *MemoryStore) Render(ctx context.Context, id, tenantID, appID string, vars map[string]any) (string, error) {
	tmpl, ok, err := s.GetScoped(ctx, id, tenantID, appID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if tmpl.Body == "" {
		return "", nil
	}
	tpl, err := pongo2.FromString(tmpl.Body)
	if err != nil {
		return "", fmt.Errorf("templates: pongo2 compile %q: %w", id, err)
	}
	ctx2 := pongo2.Context{}
	for k, v := range vars {
		ctx2[k] = v
	}
	out, err := tpl.Execute(ctx2)
	if err != nil {
		return "", fmt.Errorf("templates: pongo2 render %q: %w", id, err)
	}
	return out, nil
}

func templateVisibleToScope(item Template, tenantID string, appID string) bool {
	tenantID = strings.TrimSpace(tenantID)
	appID = strings.TrimSpace(appID)
	tenantOK := item.TenantID == "" || item.TenantID == "*" || item.TenantID == tenantID
	appOK := item.AppID == "" || item.AppID == "*" || item.AppID == appID
	return tenantOK && appOK
}

func cloneTemplate(input Template) Template {
	output := input
	output.InputDefaults = cloneMap(input.InputDefaults)
	output.OptionsDefaults = cloneMap(input.OptionsDefaults)
	return output
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func sortTemplates(items []Template) {
	sort.SliceStable(items, func(left, right int) bool {
		return items[left].ID < items[right].ID
	})
}
