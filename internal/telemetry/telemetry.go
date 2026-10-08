// Package telemetry sets up OpenTelemetry tracing and the W3C trace-context
// helpers that let one trace follow a request through the outbox, the bus and
// the webhook call (spec 13.2).
package telemetry

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var prop = propagation.TraceContext{}

// Setup installs a tracer provider and the trace-context propagator. Spans are
// exported over OTLP/HTTP when OTEL_EXPORTER_OTLP_ENDPOINT is set; otherwise
// they are still created (so trace ids propagate) and dropped.
func Setup(ctx context.Context, service string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(prop)
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName(service))),
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer is the shared tracer.
func Tracer() trace.Tracer { return otel.Tracer("crossdock") }

// Traceparent returns the W3C traceparent for the span in ctx, or "".
func Traceparent(ctx context.Context) string {
	c := propagation.MapCarrier{}
	prop.Inject(ctx, c)
	return c.Get("traceparent")
}

// WithTraceparent returns ctx carrying the remote span context from tp.
func WithTraceparent(ctx context.Context, tp string) context.Context {
	if tp == "" {
		return ctx
	}
	return prop.Extract(ctx, propagation.MapCarrier{"traceparent": tp})
}

// Inject writes the trace headers for ctx into h (any header setter).
func Inject(ctx context.Context, set func(k, v string)) {
	c := propagation.MapCarrier{}
	prop.Inject(ctx, c)
	for _, k := range c.Keys() {
		set(k, c.Get(k))
	}
}

// Extract returns ctx with the trace context found in the given header getter.
func Extract(ctx context.Context, get func(k string) string) context.Context {
	return prop.Extract(ctx, propagation.MapCarrier{"traceparent": get("traceparent"), "tracestate": get("tracestate")})
}

var secretKey = regexp.MustCompile(`(?i)(token|secret|password|passwd|authorization|cookie|signature|api[_-]?key|credential)`)

// RedactAttr is a slog ReplaceAttr that hides the value of any attribute whose name
// suggests a secret, and any value that looks like an API token (spec 17). Scanned
// codes and notes are not secrets; they stay visible as fields.
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	if secretKey.MatchString(a.Key) && a.Value.Kind() != slog.KindGroup {
		return slog.String(a.Key, "[redacted]")
	}
	if a.Value.Kind() == slog.KindString {
		if v := a.Value.String(); strings.Contains(v, "cdk_") {
			return slog.String(a.Key, strings.ReplaceAll(v, "cdk_", "cdk_[redacted]_"))
		}
	}
	return a
}
