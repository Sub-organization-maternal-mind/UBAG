package obs

import (
	"context"
	"net/http"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// forceSampleKey is an unexported context key used by SampleError to signal
// that the current span should always be recorded, regardless of the
// base TraceIDRatio sampler decision.
type forceSampleKey struct{}

// SampleError marks the current span context so the custom sampler treats it
// as always-sample. Call it when you detect an error condition before the span
// ends, so the error is captured even when the normal 10% ratio would drop it.
func SampleError(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceSampleKey{}, true)
}

// ─────────────────────────────────────────────────────────────────────────────
// Custom sampler: 100% error traces, 10% normal traces
// ─────────────────────────────────────────────────────────────────────────────

// ratioOrErrorSampler implements sdktrace.Sampler. Root spans are sampled at
// 10% via TraceIDRatioBased; spans whose context carries a force-sample flag
// (set via SampleError) are always recorded.
type ratioOrErrorSampler struct {
	ratio sdktrace.Sampler
}

func newRatioOrErrorSampler() sdktrace.Sampler {
	return sdktrace.ParentBased(
		&ratioOrErrorSampler{
			ratio: sdktrace.TraceIDRatioBased(0.10),
		},
	)
}

func (s *ratioOrErrorSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	// If the caller signalled an error on this context, always record.
	if p.ParentContext.Value(forceSampleKey{}) == true {
		return sdktrace.SamplingResult{
			Decision:   sdktrace.RecordAndSample,
			Tracestate: trace.SpanContextFromContext(p.ParentContext).TraceState(),
		}
	}
	return s.ratio.ShouldSample(p)
}

func (s *ratioOrErrorSampler) Description() string {
	return "RatioOrError{ratio=0.10}"
}

// ─────────────────────────────────────────────────────────────────────────────
// InitTracer
// ─────────────────────────────────────────────────────────────────────────────

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
		sdktrace.WithSampler(newRatioOrErrorSampler()),
		// nil resource falls back to SDK default (reads OTEL_RESOURCE_ATTRIBUTES, detects host)
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // W3C traceparent / tracestate
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP instrumentation helper
// ─────────────────────────────────────────────────────────────────────────────

// WrapWithOTel wraps an http.Handler with OpenTelemetry span extraction and
// propagation using the W3C traceparent format. Place this BEFORE the existing
// Trace middleware in the handler chain so the OTel span context is established
// first; the Trace middleware will then extract the same trace ID from the
// span context already set on the request.
//
// The operation name is set to serviceName for the server span.
func WrapWithOTel(h http.Handler, serviceName string) http.Handler {
	return otelhttp.NewHandler(h, serviceName,
		otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		)),
	)
}
