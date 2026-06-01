package ubag

import (
	"context"
	"net/http"
	"time"
)

const SidecarURL = "http://127.0.0.1:7878"

// DiscoverSidecar probes the default loopback sidecar; returns its URL or "".
func DiscoverSidecar(ctx context.Context, timeout time.Duration) string {
	return DiscoverSidecarAt(ctx, SidecarURL, timeout)
}

// DiscoverSidecarAt probes an arbitrary base URL's /v1/health endpoint.
func DiscoverSidecarAt(ctx context.Context, baseURL string, timeout time.Duration) string {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/health", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		return baseURL
	}
	return ""
}
