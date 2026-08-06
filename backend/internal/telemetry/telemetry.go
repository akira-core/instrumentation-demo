// Package telemetry configures the process-wide OpenTelemetry TracerProvider,
// MeterProvider and W3C propagator used by the backend service.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// ShutdownFunc flushes and stops the providers. Call it during process
// shutdown with a bounded-timeout context.
type ShutdownFunc func(context.Context) error

// Setup builds OTLP/HTTP exporters pointed at otlpEndpoint, wires them into a
// batching TracerProvider and a periodic-reader MeterProvider tagged with the
// given service name, installs them as the global OTel providers (plus the W3C
// tracecontext + baggage propagator), and returns a combined shutdown function.
//
// Metrics are best-effort by design: the OTLP SDK exporters buffer and retry on
// their own, and a collector that is unreachable at startup produces no error
// here — construction does not dial. So an unavailable metrics destination can
// never fail a request or block startup.
func Setup(ctx context.Context, serviceName, otlpEndpoint string) (ShutdownFunc, error) {
	endpoint, insecure, err := splitEndpoint(otlpEndpoint)
	if err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
	)

	traceOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
	metricOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(endpoint)}
	if insecure {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
	}

	traceExporter, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create otlp trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	metricExporter, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		// Traces are already live at this point; tear them back down so the
		// caller isn't left with a half-installed global state.
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry: create otlp metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// Go runtime metrics (GC, goroutines, memory) — enough to make the process
	// measurable under load without hand-writing counters. HTTP server metrics
	// come separately from the otelhttp handler wrapping the mux.
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		return nil, fmt.Errorf("telemetry: start runtime instrumentation: %w", err)
	}

	return func(shutdownCtx context.Context) error {
		return errors.Join(tp.Shutdown(shutdownCtx), mp.Shutdown(shutdownCtx))
	}, nil
}

// splitEndpoint parses an OTLP endpoint that may or may not carry an
// explicit scheme into the host:port form otlptracehttp.WithEndpoint wants,
// plus whether the connection should be established without TLS.
func splitEndpoint(raw string) (endpoint string, insecure bool, err error) {
	if raw == "" {
		return "", false, errors.New("empty OTLP endpoint")
	}

	u, parseErr := url.Parse(raw)
	if parseErr != nil || u.Host == "" {
		// Not a valid absolute URL (or no scheme) — treat the raw value as a
		// bare host:port and default to a plaintext connection, matching the
		// otlptracehttp default behavior for schemeless endpoints.
		return raw, true, nil
	}

	return u.Host, u.Scheme != "https", nil
}
