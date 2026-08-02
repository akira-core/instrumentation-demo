package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/akira-core/instrumentation-demo/backend/internal/featureflags"
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

func boolFlag(v bool) memprovider.InMemoryFlag {
	variant := "off"
	if v {
		variant = "on"
	}
	return memprovider.InMemoryFlag{
		State:          memprovider.Enabled,
		DefaultVariant: variant,
		Variants:       map[string]any{"on": true, "off": false},
	}
}

// setRelay installs an in-memory OpenFeature provider serving demo-nats-flow,
// standing in for the GOFF relay proxy. Calling it models an operator having
// set that flag on the relay.
func setRelay(t *testing.T, enabled bool) {
	t.Helper()
	if err := openfeature.SetProviderAndWait(memprovider.NewInMemoryProvider(
		map[string]memprovider.InMemoryFlag{demoNatsFlowFlagKey: boolFlag(enabled)},
	)); err != nil {
		t.Fatalf("install in-memory provider: %v", err)
	}
	t.Cleanup(func() {
		if err := openfeature.SetProviderAndWait(openfeature.NoopProvider{}); err != nil {
			t.Fatalf("reset provider: %v", err)
		}
	})
}

// noRelay models the relay proxy being unreachable / having no opinion: the
// global provider resolves nothing, so evaluations fall back to the caller's
// default (demoNatsFlowDefault).
func noRelay(t *testing.T) {
	t.Helper()
	if err := openfeature.SetProviderAndWait(openfeature.NoopProvider{}); err != nil {
		t.Fatalf("install noop provider: %v", err)
	}
}

func TestDemoTraceHandler_TraceContinuation(t *testing.T) {
	_ = setupGlobalTracing(t)
	setRelay(t, false) // flag disabled -> no NATS flow needed for this test
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)

	handler := NewDemoTraceHandler(featureflags.New(), nm, nil)

	const incomingTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	const incomingSpanID = "00f067aa0ba902b7"
	traceparent := "00-" + incomingTraceID + "-" + incomingSpanID + "-01"

	req := httptest.NewRequest(http.MethodPost, "/api/demo-trace", nil)
	req.Header.Set("traceparent", traceparent)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var got demoTraceResponse
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
	if got.FlagEnabled {
		t.Errorf("flagEnabled = true, want false (relay serves disabled)")
	}
	if got.NatsFlowExecuted {
		t.Errorf("natsFlowExecuted = true, want false (flag disabled)")
	}
}

func TestDemoTraceHandler_MissingTraceparentStartsRootTrace(t *testing.T) {
	_ = setupGlobalTracing(t)
	setRelay(t, false)
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)

	handler := NewDemoTraceHandler(featureflags.New(), nm, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/demo-trace", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var got demoTraceResponse
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
	setRelay(t, false)
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)
	handler := NewDemoTraceHandler(featureflags.New(), nm, nil)

	do := func() demoTraceResponse {
		req := httptest.NewRequest(http.MethodPost, "/api/demo-trace", nil)
		rec := httptest.NewRecorder()
		handler(rec, req)
		var got demoTraceResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return got
	}

	a, b := do(), do()
	if a.TraceID == b.TraceID {
		t.Errorf("two root-trace requests produced the same traceId %q, want distinct root traces", a.TraceID)
	}
}

func TestDemoTraceHandler_FlagEnabledButNatsUnavailable(t *testing.T) {
	_ = setupGlobalTracing(t)
	setRelay(t, true) // flag enabled -> handler must attempt the NATS round trip
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)

	handler := NewDemoTraceHandler(featureflags.New(), nm, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/demo-trace", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (nats not connected); body = %s", rec.Code, rec.Body.String())
	}
}

// With no usable provider the handler must fall back to demoNatsFlowDefault
// rather than erroring — the documented degradation when the relay proxy is
// unreachable. demoNatsFlowDefault is true, so the handler proceeds to attempt
// the NATS round trip; NATS is also unavailable here, and the resulting 503
// (rather than a flag-disabled 200) is what proves the default was applied.
func TestDemoTraceHandler_RelayUnavailableFallsBackToDefault(t *testing.T) {
	_ = setupGlobalTracing(t)
	noRelay(t)
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)

	handler := NewDemoTraceHandler(featureflags.New(), nm, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/demo-trace", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if !demoNatsFlowDefault {
		t.Skip("demoNatsFlowDefault is false; this test asserts the enabled-default path")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — with no provider the flag must default to enabled and attempt the NATS round trip; body = %s",
			rec.Code, rec.Body.String())
	}
}

func TestDemoTraceHandler_MethodNotAllowed(t *testing.T) {
	_ = setupGlobalTracing(t)
	setRelay(t, false)
	nm := natsflow.NewManager("nats://127.0.0.1:0", nil)
	handler := NewDemoTraceHandler(featureflags.New(), nm, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/demo-trace", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
