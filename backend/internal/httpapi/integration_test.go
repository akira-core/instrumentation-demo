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

// setLibraryTracingFlag installs an in-memory provider serving otelnats's
// otel-nats-tracing flag, standing in for the GOFF relay proxy.
func setLibraryTracingFlag(t *testing.T, natsTracing bool) {
	t.Helper()
	if err := openfeature.SetProviderAndWait(memprovider.NewInMemoryProvider(
		map[string]memprovider.InMemoryFlag{
			flagKeyNATSTracing: boolFlag(natsTracing),
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
// (embedded) NATS server, covering the full publish/consume/reply round trip.
func TestDemoTraceHandler_FullNatsRoundTrip(t *testing.T) {
	_ = setupGlobalTracing(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "1")

	setLibraryTracingFlag(t, true)
	t.Cleanup(func() { _ = openfeature.SetProviderAndWait(openfeature.NoopProvider{}) })

	nm := startNATS(t)
	handler := NewDemoTraceHandler(nm, nil)

	got := postDemoTrace(t, handler)

	if !traceIDPattern.MatchString(got.TraceID) {
		t.Errorf("traceId = %q, want 32 hex chars", got.TraceID)
	}
	if !spanIDPattern.MatchString(got.SpanID) {
		t.Errorf("spanId = %q, want 16 hex chars", got.SpanID)
	}
}

// TestRelayFlagTogglesNatsInstrumentation is the regression test for this
// demo's headline capability: an operator flipping otel-nats-tracing on the
// relay proxy turns otelnats's spans on and off in a running process, with no
// restart and no application code involved in the decision. The NATS business
// path still completes in every phase.
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
	setLibraryTracingFlag(t, true)
	time.Sleep(relaySnapshotTTL)

	nm := startNATS(t)
	handler := NewDemoTraceHandler(nm, nil)

	_ = postDemoTrace(t, handler)
	tracedOn := countNATSSpans(rec.Ended())
	if tracedOn == 0 {
		t.Fatalf("phase 1: no NATS spans emitted while the relay served otel-nats-tracing=enabled; "+
			"got %d spans overall", len(rec.Ended()))
	}

	// Phase 2: operator flips the flag OFF on the relay. Same process, same
	// connection, no restart. Business path must still succeed.
	setLibraryTracingFlag(t, false)
	time.Sleep(relaySnapshotTTL)

	before := countNATSSpans(rec.Ended())
	_ = postDemoTrace(t, handler)
	if after := countNATSSpans(rec.Ended()); after != before {
		t.Errorf("phase 2: %d new NATS spans emitted after the relay disabled otel-nats-tracing, want 0 — "+
			"is the connection pinned with otelnats.WithTracingEnabled()?", after-before)
	}

	// Phase 3: flip it back ON; instrumentation must return.
	setLibraryTracingFlag(t, true)
	time.Sleep(relaySnapshotTTL)

	before = countNATSSpans(rec.Ended())
	_ = postDemoTrace(t, handler)
	if after := countNATSSpans(rec.Ended()); after == before {
		t.Errorf("phase 3: no new NATS spans after re-enabling otel-nats-tracing on the relay, want > 0")
	}
}
