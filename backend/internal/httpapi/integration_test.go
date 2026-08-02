package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/akira-core/instrumentation-demo/backend/internal/featureflags"
	"github.com/akira-core/instrumentation-demo/backend/internal/natsflow"
)

// otelnats's own flag key and env vars. Duplicated here rather than imported
// because they are unexported in that package; they are part of its documented
// public contract (see its README's dynamic-flags table).
const (
	flagKeyNATSTracing      = "otel-nats-tracing"
	envGlobalTracingEnabled = "OTEL_INSTRUMENTATION_GO_TRACING_ENABLED"
	envNATSTracingEnabled   = "OTEL_NATS_TRACING_ENABLED"
)

// otelnats caches its resolved flag snapshot for one second, so a test that
// flips the relay mid-run must outwait that TTL before the next read consults
// the new value.
const relaySnapshotTTL = 1100 * time.Millisecond

// startNATS runs an embedded NATS server and a connected natsflow.Manager.
func startNATS(t *testing.T) *natsflow.Manager {
	t.Helper()

	srv := natstest.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)

	nm := natsflow.NewManager(srv.ClientURL(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	nm.Start(ctx)
	t.Cleanup(nm.Close)

	deadline := time.Now().Add(5 * time.Second)
	for !nm.Connected() {
		if time.Now().After(deadline) {
			t.Fatal("natsflow manager did not connect in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nm
}

// setRelayFlags installs an in-memory provider serving both the application
// flag and otelnats's own tracing flag, standing in for the GOFF relay proxy.
func setRelayFlags(t *testing.T, demoFlow, natsTracing bool) {
	t.Helper()
	if err := openfeature.SetProviderAndWait(memprovider.NewInMemoryProvider(
		map[string]memprovider.InMemoryFlag{
			demoNatsFlowFlagKey: boolFlag(demoFlow),
			flagKeyNATSTracing:  boolFlag(natsTracing),
		},
	)); err != nil {
		t.Fatalf("install in-memory provider: %v", err)
	}
}

func countNATSSpans(spans []sdktrace.ReadOnlySpan) int {
	n := 0
	for _, s := range spans {
		if strings.Contains(s.Name(), "demo.trace.") {
			n++
		}
	}
	return n
}

func postDemoTrace(t *testing.T, handler http.HandlerFunc) demoTraceResponse {
	t.Helper()
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
	return got
}

// TestDemoTraceHandler_FullNatsRoundTrip exercises the handler against a real
// (embedded) NATS server with the relay reporting demo-nats-flow enabled,
// covering the full publish(demo.trace.request)/consume/reply(demo.trace.reply)
// round trip end to end (task 2.10).
func TestDemoTraceHandler_FullNatsRoundTrip(t *testing.T) {
	_ = setupGlobalTracing(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "1")

	setRelayFlags(t, true, true)
	t.Cleanup(func() { _ = openfeature.SetProviderAndWait(openfeature.NoopProvider{}) })

	nm := startNATS(t)
	handler := NewDemoTraceHandler(featureflags.New(), nm, nil)

	got := postDemoTrace(t, handler)

	if !got.FlagEnabled {
		t.Errorf("flagEnabled = false, want true")
	}
	if !got.NatsFlowExecuted {
		t.Errorf("natsFlowExecuted = false, want true (round trip should have completed)")
	}
	if !traceIDPattern.MatchString(got.TraceID) {
		t.Errorf("traceId = %q, want 32 hex chars", got.TraceID)
	}
}

// TestRelayFlagTogglesNatsInstrumentation is the regression test for this
// demo's headline capability: an operator flipping otel-nats-tracing on the
// relay proxy turns otelnats's spans on and off in a running process, with no
// restart and no application code involved in the decision.
//
// It is also the guard against silently reintroducing
// otelnats.WithTracingEnabled(...) in natsflow: that option pins a connection
// static, so the "off" phase below would keep emitting spans and this test
// would fail.
//
// The environment deliberately says tracing is OFF for the module while the
// kill switch is ON, so every span observed here is attributable to the relay
// rather than to the environment.
func TestRelayFlagTogglesNatsInstrumentation(t *testing.T) {
	rec := setupGlobalTracing(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "false")

	t.Cleanup(func() {
		_ = openfeature.SetProviderAndWait(openfeature.NoopProvider{})
		time.Sleep(relaySnapshotTTL) // don't leave a stale snapshot for sibling tests
	})

	// Phase 1: relay says tracing ON.
	setRelayFlags(t, true, true)
	time.Sleep(relaySnapshotTTL)

	nm := startNATS(t)
	handler := NewDemoTraceHandler(featureflags.New(), nm, nil)

	if got := postDemoTrace(t, handler); !got.NatsFlowExecuted {
		t.Fatalf("phase 1: natsFlowExecuted = false, want true")
	}
	tracedOn := countNATSSpans(rec.Ended())
	if tracedOn == 0 {
		t.Fatalf("phase 1: no NATS spans emitted while the relay served otel-nats-tracing=enabled; "+
			"got %d spans overall", len(rec.Ended()))
	}

	// Phase 2: operator flips the flag OFF on the relay. Same process, same
	// connection, no restart.
	setRelayFlags(t, true, false)
	time.Sleep(relaySnapshotTTL)

	before := countNATSSpans(rec.Ended())
	if got := postDemoTrace(t, handler); !got.NatsFlowExecuted {
		t.Fatalf("phase 2: natsFlowExecuted = false, want true (the round trip must still run; only its instrumentation is disabled)")
	}
	if after := countNATSSpans(rec.Ended()); after != before {
		t.Errorf("phase 2: %d new NATS spans emitted after the relay disabled otel-nats-tracing, want 0 — "+
			"is the connection pinned with otelnats.WithTracingEnabled()?", after-before)
	}

	// Phase 3: flip it back ON; instrumentation must return.
	setRelayFlags(t, true, true)
	time.Sleep(relaySnapshotTTL)

	before = countNATSSpans(rec.Ended())
	if got := postDemoTrace(t, handler); !got.NatsFlowExecuted {
		t.Fatalf("phase 3: natsFlowExecuted = false, want true")
	}
	if after := countNATSSpans(rec.Ended()); after == before {
		t.Errorf("phase 3: no new NATS spans after re-enabling otel-nats-tracing on the relay, want > 0")
	}
}
