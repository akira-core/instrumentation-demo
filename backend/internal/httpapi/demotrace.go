package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/akira-core/instrumentation-demo/backend/internal/featureflags"
	"github.com/akira-core/instrumentation-demo/backend/internal/natsflow"
)

// demoNatsFlowFlagKey is the relay-proxy flag that gates the NATS
// publish/consume/reply sub-flow. It is the application-level flag; the
// library-level counterpart (otel-nats-tracing, which decides whether NATS
// spans exist at all) is read by otelnats itself, not here.
const demoNatsFlowFlagKey = "demo-nats-flow"

// demoNatsFlowDefault is the value used when the relay proxy has no usable
// opinion — unreachable, still starting, or flag undefined. Defaulting to
// enabled keeps the demo working out of the box in that degraded state.
const demoNatsFlowDefault = true

// natsRoundTripTimeout bounds how long the handler waits for the NATS
// reply before giving up (task 2.6).
const natsRoundTripTimeout = 5 * time.Second

const tracerName = "github.com/akira-core/instrumentation-demo/backend/httpapi"

// demoTraceResponse is the fixed success response shape for
// POST /api/demo-trace per the integration contract.
type demoTraceResponse struct {
	TraceID          string `json:"traceId"`
	SpanID           string `json:"spanId"`
	FlagEnabled      bool   `json:"flagEnabled"`
	NatsFlowExecuted bool   `json:"natsFlowExecuted"`
}

type errorResponse struct {
	Error   string `json:"error"`
	TraceID string `json:"traceId,omitempty"`
	SpanID  string `json:"spanId,omitempty"`
}

// NewDemoTraceHandler builds the POST /api/demo-trace handler. It:
//  1. extracts an incoming W3C trace context (or starts a root trace),
//  2. opens a SERVER span for the request,
//  3. evaluates the "demo-nats-flow" flag against the GOFF relay proxy through
//     OpenFeature,
//  4. when enabled, runs the full NATS publish/consume/reply round trip and
//     waits for it before responding.
func NewDemoTraceHandler(flags *featureflags.Client, nm *natsflow.Manager, logger *slog.Logger) http.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	tracer := otel.Tracer(tracerName)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract W3C trace context from the incoming request; if none is
		// present, propagation.TraceContext leaves ctx unchanged and the
		// subsequent tracer.Start below begins a new root trace.
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		ctx, span := tracer.Start(ctx, "POST /api/demo-trace", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		span.SetAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("url.path", r.URL.Path),
		)

		sc := span.SpanContext()
		traceID := sc.TraceID().String()
		spanID := sc.SpanID().String()

		flagEnabled := flags.Enabled(ctx, demoNatsFlowFlagKey, demoNatsFlowDefault)
		span.SetAttributes(attribute.Bool("demo.flag.enabled", flagEnabled))

		resp := demoTraceResponse{
			TraceID:     traceID,
			SpanID:      spanID,
			FlagEnabled: flagEnabled,
		}

		if !flagEnabled {
			writeJSON(w, http.StatusOK, resp)
			return
		}

		if err := nm.RunRoundTrip(ctx, natsRoundTripTimeout); err != nil {
			logger.ErrorContext(ctx, "demo-trace: nats round trip failed", "error", err)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())

			status := http.StatusGatewayTimeout
			if errors.Is(err, natsflow.ErrNotConnected) {
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, errorResponse{
				Error:   err.Error(),
				TraceID: traceID,
				SpanID:  spanID,
			})
			return
		}

		resp.NatsFlowExecuted = true
		writeJSON(w, http.StatusOK, resp)
	}
}
