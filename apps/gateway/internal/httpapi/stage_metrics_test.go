package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMetricStageAndWebhookDeliveryHistograms(t *testing.T) {
	server := NewServer(Config{Version: "test", AppSecret: "dev-secret"})
	server.ObserveJobStage("gemini_web", "provider_submit", 120*time.Millisecond)
	server.ObserveJobStage("gemini_web", "not_a_stage", time.Second) // dropped
	server.ObserveWebhookDelivery("none", 40*time.Millisecond)
	server.ObserveWebhookDelivery("http_5xx", 2*time.Second)
	server.ObserveWebhookDelivery("surprise", time.Second) // bounded to "other"

	body := doJSON(server.Handler(), http.MethodGet, "/v1/metrics", "", nil).Body.String()
	for _, expected := range []string{
		`# TYPE ubag_job_stage_duration_seconds histogram`,
		`ubag_job_stage_duration_seconds_count{stage="provider_submit",adapter_family="browser"} 1`,
		`ubag_job_stage_duration_seconds_sum{stage="provider_submit",adapter_family="browser"} 0.120000`,
		`ubag_job_stage_duration_seconds_count{stage="extraction",adapter_family="mock"} 0`,
		`ubag_webhook_deliveries_total{endpoint_kind="job_callback",outcome="success",error_class="none"} 1`,
		`ubag_webhook_deliveries_total{endpoint_kind="job_callback",outcome="failure",error_class="http_5xx"} 1`,
		`ubag_webhook_deliveries_total{endpoint_kind="job_callback",outcome="failure",error_class="other"} 1`,
		`ubag_webhook_delivery_duration_seconds_count{endpoint_kind="job_callback",outcome="success"} 1`,
		`ubag_webhook_delivery_duration_seconds_count{endpoint_kind="job_callback",outcome="failure"} 2`,
		`ubag_webhook_delivery_duration_seconds_sum{endpoint_kind="job_callback",outcome="success"} 0.040000`,
		`ubag_gateway_ready{service="ubag-gateway",check="jobs"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "not_a_stage") || strings.Contains(body, "ubag_adapter_requests_total") {
		t.Fatalf("metrics contain a dropped stage or a hard-coded adapter stub:\n%s", body)
	}
}

func TestMetricRouteLatencyCarriesMethodClass(t *testing.T) {
	server := NewServer(Config{Version: "test", AppSecret: "dev-secret"})
	handler := server.Handler()
	doJSON(handler, http.MethodGet, "/v1/health", "", nil)
	doJSON(handler, http.MethodPost, "/v1/health", "", nil)

	body := doJSON(handler, http.MethodGet, "/v1/metrics", "", nil).Body.String()
	for _, expected := range []string{
		`ubag_gateway_request_latency_seconds_count{route="/v1/health",method_class="read"} 1`,
		`ubag_gateway_request_latency_seconds_count{route="/v1/health",method_class="write"} 1`,
		`method="GET",status_class="2xx",method_class="read"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, body)
		}
	}
	for method, want := range map[string]string{"GET": "read", "HEAD": "read", "POST": "write", "DELETE": "write", "OPTIONS": "other", "OTHER": "other"} {
		if got := metricMethodClass(method); got != want {
			t.Fatalf("metricMethodClass(%s) = %s, want %s", method, got, want)
		}
	}
}
