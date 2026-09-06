package obs

import (
	"context"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// InitTracer initialises the OTel tracer provider and returns a shutdown
// function. When UBAG_OTLP_ENDPOINT is unset the global tracer is set to a
// no-op provider and the returned shutdown is a harmless no-op.
// Call once from serve.Run; defer the returned shutdown.
func InitTracer(ctx context.Context) (shutdown func(context.Context) error, err error) {
	endpoint := strings.TrimSpace(os.Getenv("UBAG_OTLP_ENDPOINT"))
	if endpoint == "" {
		// No-op: set the global to a no-op provider so instrumented code does not
		// need to guard against a nil tracer.
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		return func(context.Context) error { return nil }, nil
	}

	// Build the OTLP/gRPC exporter pointing at the configured endpoint.
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.10))),
		// nil resource falls back to SDK default (reads OTEL_RESOURCE_ATTRIBUTES, detects host)
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // W3C traceparent / tracestate
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
