// Package httpapi wires the backend's HTTP handlers: the traced demo
// endpoint, CORS handling for browser callers, and the health probe.
package httpapi

import (
	"log/slog"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/akira-core/instrumentation-demo/backend/internal/natsflow"
)

// NewMux builds the backend's top-level http.Handler.
//
// The demo endpoint is wrapped with otelhttp purely for server-side request
// METRICS (duration, in-flight count, request/response size), which it reports
// through the global MeterProvider.
//
// Its span creation is explicitly disabled by handing it a noop TracerProvider.
// NewDemoTraceHandler already extracts the incoming W3C context and opens its
// own SERVER span; letting otelhttp open one too would emit a second SERVER span
// per request and change the shape of the trace that the trace-propagation specs
// pin down. A noop tracer keeps the verified trace path untouched while still
// producing the metrics this pipeline exists to collect.
//
// /healthz is deliberately left unwrapped: kubelet probes it every few seconds
// and would otherwise dominate the request metrics with traffic nobody cares
// about.
func NewMux(corsAllowedOrigin string, nm *natsflow.Manager, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	demo := withCORS(corsAllowedOrigin, NewDemoTraceHandler(nm, logger))
	mux.Handle("/api/demo-trace", otelhttp.NewHandler(demo, "POST /api/demo-trace",
		otelhttp.WithTracerProvider(noop.NewTracerProvider()),
	))
	mux.HandleFunc("/healthz", NewHealthzHandler(nm))

	return mux
}
