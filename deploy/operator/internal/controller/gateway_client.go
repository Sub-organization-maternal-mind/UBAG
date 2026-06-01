package controller

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // md5 used for non-cryptographic spec hashing
	"encoding/json"
	"fmt"
	"net/http"

	v1alpha1 "github.com/ubag/ubag/deploy/operator/api/v1alpha1"
)

// GatewayClientInterface is the abstraction over the gateway REST API used by reconcilers.
// It is implemented by GatewayClient (production) and mockGateway (tests).
type GatewayClientInterface interface {
	CreateOrUpdateTarget(ctx context.Context, spec v1alpha1.TargetSpec) error
	DeleteTarget(ctx context.Context, name string) error

	CreateOrUpdateAdapter(ctx context.Context, spec v1alpha1.AdapterSpec) error
	DeleteAdapter(ctx context.Context, name string) error

	CreateOrUpdateTemplate(ctx context.Context, spec v1alpha1.TemplateSpec) error
	DeleteTemplate(ctx context.Context, name string) error

	CreateOrUpdateApp(ctx context.Context, spec v1alpha1.AppSpec) error
	DeleteApp(ctx context.Context, name string) error
}

// GatewayClient is the production HTTP client for the UBAG gateway REST API.
type GatewayClient struct {
	BaseURL    string
	AppSecret  string
	HTTPClient *http.Client
}

// NewGatewayClient returns a production GatewayClient.
func NewGatewayClient(baseURL, appSecret string) *GatewayClient {
	return &GatewayClient{
		BaseURL:    baseURL,
		AppSecret:  appSecret,
		HTTPClient: &http.Client{},
	}
}

// HashSpec returns a 12-character hex hash of any spec struct for idempotency comparison.
// Uses MD5 for speed — this is NOT used for security purposes.
func HashSpec(spec interface{}) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := md5.Sum(data) //nolint:gosec
	return fmt.Sprintf("%x", sum)[:12], nil
}

// do encodes body as JSON, sets Authorization header, executes the request,
// and returns an error for any non-2xx response.
func (c *GatewayClient) do(ctx context.Context, method, path string, body interface{}) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return fmt.Errorf("gateway_client: marshal body: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, &buf)
	if err != nil {
		return fmt.Errorf("gateway_client: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.AppSecret)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("gateway_client: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gateway_client: %s %s returned %d", method, path, resp.StatusCode)
	}
	return nil
}

// --- Target ---

func (c *GatewayClient) CreateOrUpdateTarget(ctx context.Context, spec v1alpha1.TargetSpec) error {
	return c.do(ctx, http.MethodPost, "/v1/targets", map[string]interface{}{
		"name":  spec.Name,
		"url":   spec.URL,
		"model": spec.Model,
		"tags":  spec.Tags,
	})
}

func (c *GatewayClient) DeleteTarget(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/targets/"+name, nil)
}

// --- Adapter ---

func (c *GatewayClient) CreateOrUpdateAdapter(ctx context.Context, spec v1alpha1.AdapterSpec) error {
	return c.do(ctx, http.MethodPost, "/v1/adapters", map[string]interface{}{
		"name":   spec.Name,
		"type":   spec.Type,
		"config": spec.Config,
	})
}

func (c *GatewayClient) DeleteAdapter(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/adapters/"+name, nil)
}

// --- Template ---

func (c *GatewayClient) CreateOrUpdateTemplate(ctx context.Context, spec v1alpha1.TemplateSpec) error {
	return c.do(ctx, http.MethodPost, "/v1/templates", map[string]interface{}{
		"name":      spec.Name,
		"content":   spec.Content,
		"variables": spec.Variables,
	})
}

func (c *GatewayClient) DeleteTemplate(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/templates/"+name, nil)
}

// --- App ---

func (c *GatewayClient) CreateOrUpdateApp(ctx context.Context, spec v1alpha1.AppSpec) error {
	return c.do(ctx, http.MethodPost, "/v1/apps", map[string]interface{}{
		"name":        spec.Name,
		"description": spec.Description,
		"targets":     spec.Targets,
	})
}

func (c *GatewayClient) DeleteApp(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/apps/"+name, nil)
}
