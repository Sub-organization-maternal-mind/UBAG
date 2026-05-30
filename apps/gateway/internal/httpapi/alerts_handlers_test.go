package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/alerts"
)

func newTestAlertManager() *alerts.Manager {
	summary := alerts.ConfigSummary{SinkType: "log", StoreKind: "memory", RecipientCount: 1, Recipients: []string{alerts.DefaultRecipient}}
	return alerts.NewManager(alerts.NewMemoryStore(), nil, nil, summary)
}

func TestAlertRoutesReturn501WhenUnconfigured(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "admin"}).Handler()

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/alerts"},
		{http.MethodGet, "/v1/alerts/config"},
		{http.MethodPost, "/v1/alerts/alert_x/acknowledge"},
		{http.MethodPost, "/v1/alerts/alert_x/resolve"},
	}
	for _, tc := range cases {
		resp := doJSON(server, tc.method, tc.path, "", authHeaders("alert-key-000000000001"))
		if resp.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d, want 501; body=%s", tc.method, tc.path, resp.Code, resp.Body.String())
		}
	}
}

func TestAlertListAcknowledgeResolveHappyPath(t *testing.T) {
	manager := newTestAlertManager()
	raised, err := manager.RaiseManualAction(context.Background(), alerts.Alert{
		TenantID: defaultTenantID,
		AppID:    defaultAppID,
		JobID:    "job-1",
		Kind:     alerts.KindCaptcha,
		Message:  "solve it",
	})
	if err != nil {
		t.Fatalf("seed alert: %v", err)
	}
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "operator", Alerts: manager}).Handler()

	list := doJSON(server, http.MethodGet, "/v1/alerts", "", authHeaders(""))
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d; body=%s", list.Code, list.Body.String())
	}
	var listResp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Data) != 1 || listResp.Data[0]["alert_id"] != raised.AlertID {
		t.Fatalf("unexpected list payload: %+v", listResp.Data)
	}

	ack := doJSON(server, http.MethodPost, "/v1/alerts/"+raised.AlertID+"/acknowledge", "", authHeaders(""))
	if ack.Code != http.StatusOK {
		t.Fatalf("acknowledge = %d; body=%s", ack.Code, ack.Body.String())
	}
	var ackResp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(ack.Body.Bytes(), &ackResp); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ackResp.Data["status"] != alerts.StatusAcknowledged {
		t.Fatalf("status after ack = %v", ackResp.Data["status"])
	}

	resolve := doJSON(server, http.MethodPost, "/v1/alerts/"+raised.AlertID+"/resolve", "", authHeaders(""))
	if resolve.Code != http.StatusOK {
		t.Fatalf("resolve = %d; body=%s", resolve.Code, resolve.Body.String())
	}

	cfg := doJSON(server, http.MethodGet, "/v1/alerts/config", "", authHeaders(""))
	if cfg.Code != http.StatusOK {
		t.Fatalf("config = %d; body=%s", cfg.Code, cfg.Body.String())
	}
	var cfgResp map[string]any
	if err := json.Unmarshal(cfg.Body.Bytes(), &cfgResp); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfgResp["sink_type"] != "log" || cfgResp["recipient_count"].(float64) != 1 {
		t.Fatalf("unexpected config payload: %+v", cfgResp)
	}
	// The config response must never expose SMTP credentials.
	if _, leaked := cfgResp["smtp_password"]; leaked {
		t.Fatalf("config leaked smtp_password")
	}
}

func TestAlertListForbiddenForViewer(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "viewer", Alerts: newTestAlertManager()}).Handler()
	resp := doJSON(server, http.MethodGet, "/v1/alerts", "", authHeaders(""))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("viewer list = %d, want 403; body=%s", resp.Code, resp.Body.String())
	}
}

func TestAlertAcknowledgeUnknownReturns404(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "operator", Alerts: newTestAlertManager()}).Handler()
	resp := doJSON(server, http.MethodPost, "/v1/alerts/missing/acknowledge", "", authHeaders(""))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("unknown ack = %d, want 404; body=%s", resp.Code, resp.Body.String())
	}
}
