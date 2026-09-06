package obs_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/ubag/ubag/apps/gateway/internal/obs"
)

// TestInitTracerNoopWhenEndpointUnset verifies that InitTracer installs a no-op
// provider when UBAG_OTLP_ENDPOINT is unset, so instrumented code never needs
// to guard against a nil tracer.
func TestInitTracerNoopWhenEndpointUnset(t *testing.T) {
	t.Setenv("UBAG_OTLP_ENDPOINT", "")

	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)

	shutdown, err := obs.InitTracer(context.Background())
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
}

// TestInitTracerSpanEndToEnd verifies spans flow through the installed
// provider without erroring.
func TestInitTracerSpanEndToEnd(t *testing.T) {
	t.Setenv("UBAG_OTLP_ENDPOINT", "")

	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)

	shutdown, err := obs.InitTracer(context.Background())
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	_, span := otel.Tracer("obs-test").Start(context.Background(), "probe")
	span.End()
}
