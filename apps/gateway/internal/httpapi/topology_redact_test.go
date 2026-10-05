package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

func topologyInstanceResponse(t *testing.T) map[string]any {
	t.Helper()
	store := topology.NewMemoryStore()
	store.AddInstance(topology.BrowserInstance{InstanceID: "inst-cdp", WorkerID: "w1", TenantID: defaultTenantID, Engine: "chromium", State: "ready", RemoteEndpoint: "http://172.28.0.10:9223", CreatedAt: time.Now()})
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Topology: store}).Handler()
	resp := doJSON(server, http.MethodGet, "/v1/browser/instances", "", authHeaders(""))
	if resp.Code != http.StatusOK {
		t.Fatalf("instances = %d; body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil || len(payload.Data) != 1 {
		t.Fatalf("decode: %v data=%v", err, payload.Data)
	}
	return payload.Data[0]
}

// Default (legacy) keeps remote_endpoint; UBAG_REDACT_REMOTE_ENDPOINT=true drops it.
func TestTopologyInstanceRemoteEndpointRedaction(t *testing.T) {
	t.Setenv("UBAG_REDACT_REMOTE_ENDPOINT", "")
	if got := topologyInstanceResponse(t)["remote_endpoint"]; got != "http://172.28.0.10:9223" {
		t.Fatalf("default must keep legacy remote_endpoint, got %v", got)
	}
	t.Setenv("UBAG_REDACT_REMOTE_ENDPOINT", "true")
	if _, present := topologyInstanceResponse(t)["remote_endpoint"]; present {
		t.Fatal("remote_endpoint must be absent when UBAG_REDACT_REMOTE_ENDPOINT=true")
	}
}
