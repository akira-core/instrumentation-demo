package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/akira-core/instrumentation-demo/backend/internal/natsflow"
)

var (
	traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	spanIDPattern  = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// setupGlobalTracing installs a real (in-memory-recording) TracerProvider
// and the W3C propagator as the process globals for the duration of the
// test, so span contexts produced by the handler under test are real
// (non-zero) trace/span IDs. The returned recorder exposes every span that
// ended, which lets tests assert on instrumentation actually being emitted.
func setupGlobalTracing(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}))

	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec
}

func TestDemoTraceHandler_TraceContinuation(t *testing.T) {
	_ = setupGlobalTracing(t)
	// NATS unavailable: still asserts W3C continuation from the error response's
	// traceId/spanId (handler always attempts the round trip).
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)
	handler := NewDemoTraceHandler(nm, nil)

	const incomingTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	const incomingSpanID = "00f067aa0ba902b7"
	traceparent := "00-" + incomingTraceID + "-" + incomingSpanID + "-01"

	req := httptest.NewRequest(http.MethodPost, "/api/demo-trace", nil)
	req.Header.Set("traceparent", traceparent)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (nats not connected); body = %s", rec.Code, rec.Body.String())
	}

	var got errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got.TraceID != incomingTraceID {
		t.Errorf("traceId = %q, want continuation of incoming trace %q", got.TraceID, incomingTraceID)
	}
	if !spanIDPattern.MatchString(got.SpanID) {
		t.Errorf("spanId = %q, want 16 hex chars", got.SpanID)
	}
	if got.SpanID == incomingSpanID {
		t.Errorf("spanId = %q, want a NEW span id (child of the incoming span), not the incoming span id itself", got.SpanID)
	}
}

func TestDemoTraceHandler_MissingTraceparentStartsRootTrace(t *testing.T) {
	_ = setupGlobalTracing(t)
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)
	handler := NewDemoTraceHandler(nm, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/demo-trace", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	// NATS down → 503, but body still carries a new root trace ID.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rec.Code, rec.Body.String())
	}

	var got errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !traceIDPattern.MatchString(got.TraceID) {
		t.Errorf("traceId = %q, want 32 hex chars", got.TraceID)
	}
	if !spanIDPattern.MatchString(got.SpanID) {
		t.Errorf("spanId = %q, want 16 hex chars", got.SpanID)
	}
}

func TestDemoTraceHandler_TwoRequestsWithoutTraceparentGetDifferentTraceIDs(t *testing.T) {
	_ = setupGlobalTracing(t)
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)
	handler := NewDemoTraceHandler(nm, nil)

	do := func() string {
		req := httptest.NewRequest(http.MethodPost, "/api/demo-trace", nil)
		rec := httptest.NewRecorder()
		handler(rec, req)
		var got errorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return got.TraceID
	}

	a, b := do(), do()
	if a == b {
		t.Errorf("two root-trace requests produced the same traceId %q, want distinct root traces", a)
	}
}

func TestDemoTraceHandler_NatsUnavailable(t *testing.T) {
	_ = setupGlobalTracing(t)
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)
	handler := NewDemoTraceHandler(nm, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/demo-trace", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (nats not connected); body = %s", rec.Code, rec.Body.String())
	}
}

func TestDemoTraceHandler_MethodNotAllowed(t *testing.T) {
	_ = setupGlobalTracing(t)
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)
	handler := NewDemoTraceHandler(nm, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/demo-trace", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
