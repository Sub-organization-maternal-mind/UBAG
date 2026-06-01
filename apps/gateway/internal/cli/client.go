// Package cli provides a stdlib-only REST client and command layer for the
// UBAG gateway.  It has no third-party dependencies so it can be imported by
// cmd/ubag/main.go without pulling in the heavy internal packages.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// HealthResponse is a minimal projection of GET /v1/health.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

// VersionResponse is a minimal projection of GET /v1/version.
type VersionResponse struct {
	Version     string   `json:"version"`
	APIVersions []string `json:"api_versions"`
}

// JobResponse is a minimal projection of a job envelope.
// The server uses "job_id" for the identifier field.
type JobResponse struct {
	ID     string `json:"job_id"`
	Status string `json:"status"`
	Target string `json:"target,omitempty"`
}

// TargetResponse is a minimal projection of a target object.
type TargetResponse struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// CreateJobRequest is the minimal payload sent when creating a job via the CLI.
type CreateJobRequest struct {
	Target      string `json:"target"`
	Prompt      string `json:"prompt"`
	CommandType string `json:"command_type,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Client
// ─────────────────────────────────────────────────────────────────────────────

// Client is a thin HTTP client for the UBAG gateway REST API.
type Client struct {
	BaseURL    string
	AppSecret  string
	APIVersion string
	HTTPClient *http.Client
}

// NewClient returns a Client with sensible defaults.
func NewClient(baseURL, appSecret, apiVersion string) *Client {
	if apiVersion == "" {
		apiVersion = DefaultAPIVersion
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		AppSecret:  appSecret,
		APIVersion: apiVersion,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Ubag-Api-Version", c.APIVersion)
	if c.AppSecret != "" {
		req.Header.Set("Authorization", "Bearer "+c.AppSecret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// ─────────────────────────────────────────────────────────────────────────────
// API methods
// ─────────────────────────────────────────────────────────────────────────────

// Health calls GET /v1/health.
func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/health", nil)
	if err != nil {
		return HealthResponse{}, err
	}
	var out HealthResponse
	return out, c.do(req, &out)
}

// Version calls GET /v1/version.
func (c *Client) Version(ctx context.Context) (VersionResponse, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/version", nil)
	if err != nil {
		return VersionResponse{}, err
	}
	var out VersionResponse
	return out, c.do(req, &out)
}

// CreateJob calls POST /v1/jobs.
func (c *Client) CreateJob(ctx context.Context, jobReq CreateJobRequest) (JobResponse, error) {
	b, err := json.Marshal(jobReq)
	if err != nil {
		return JobResponse{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	if err != nil {
		return JobResponse{}, err
	}
	var out JobResponse
	return out, c.do(req, &out)
}

// GetJob calls GET /v1/jobs/{id}.
func (c *Client) GetJob(ctx context.Context, id string) (JobResponse, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/jobs/"+id, nil)
	if err != nil {
		return JobResponse{}, err
	}
	var out JobResponse
	return out, c.do(req, &out)
}

// listJobsEnvelope mirrors the server's listJobsResponse for unmarshalling.
type listJobsEnvelope struct {
	Jobs []JobResponse `json:"jobs"`
}

// ListJobs calls GET /v1/jobs.
func (c *Client) ListJobs(ctx context.Context) ([]JobResponse, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/jobs", nil)
	if err != nil {
		return nil, err
	}
	var env listJobsEnvelope
	if err := c.do(req, &env); err != nil {
		return nil, err
	}
	return env.Jobs, nil
}

// WatchJob opens a GET /v1/sse/jobs/{id} SSE stream and calls handler for
// each non-empty line until the stream ends or ctx is cancelled.
func (c *Client) WatchJob(ctx context.Context, id string, handler func(event string)) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/sse/jobs/"+id, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			handler(line)
		}
		// Check context cancellation between lines.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return scanner.Err()
}

// listTargetsEnvelope handles both a raw array and a wrapped response.
type listTargetsEnvelope struct {
	Data []TargetResponse `json:"data"`
}

// ListTargets calls GET /v1/targets.
func (c *Client) ListTargets(ctx context.Context) ([]TargetResponse, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/targets", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	// Try raw array first, then wrapped envelope.
	var arr []TargetResponse
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr, nil
	}
	var env listTargetsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// PurgeCache calls POST /v1/cache/invalidate.
func (c *Client) PurgeCache(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/cache/invalidate", nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}
