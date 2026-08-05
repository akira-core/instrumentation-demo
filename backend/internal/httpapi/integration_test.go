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

// otelnats's own flag key, OpenFeature domain and env vars. Duplicated here
// rather than imported because they are unexported in that package; they are
// part of its documented public contract (see its feature-flags.md).
const (
	flagKeyNATSTracing      = "otel-nats-tracing"
	flagDomain              = "otel-instrumentation-go"
	envGlobalTracingEnabled = "OTEL_INSTRUMENTATION_GO_TRACING_ENABLED"
	envNATSTracingEnabled   = "OTEL_NATS_TRACING_ENABLED"
	envFlagsEndpoint        = "OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT"
)

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
//
// It binds to otelnats's NAMED domain rather than installing a default
// provider. A named provider outranks the default for the library's clients,
// so this cannot be shadowed by anything else in the binary having installed
// one first. There is nothing to wait for afterwards: 0.8.0 removed the
// resolver's snapshot cache, so a rebound provider is observed on the very next
// instrumented operation.
func setLibraryTracingFlag(t *testing.T, natsTracing bool) {
	t.Helper()
	if err := openfeature.SetNamedProviderAndWait(flagDomain, memprovider.NewInMemoryProvider(
		map[string]memprovider.InMemoryFlag{
			flagKeyNATSTracing: boolFlag(natsTracing),
		},
	)); err != nil {
		t.Fatalf("install in-memory provider: %v", err)
	}
	t.Cleanup(func() { _ = openfeature.SetNamedProviderAndWait(flagDomain, openfeature.NoopProvider{}) })
}

// isolateFromRelay blanks the endpoint variable that would otherwise make
// otelnats build a real GO Feature Flag provider on its first evaluation and
// reach the network. An empty value is the library's "no provider is installed"
// state, so this is a guard against a developer's shell, not a behavior change.
func isolateFromRelay(t *testing.T) {
	t.Helper()
	t.Setenv(envFlagsEndpoint, "")
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
// Matches deployment posture: module env off, relay (in-memory stand-in) on.
func TestDemoTraceHandler_FullNatsRoundTrip(t *testing.T) {
	_ = setupGlobalTracing(t)
	isolateFromRelay(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "false")

	setLibraryTracingFlag(t, true)

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
// relay turns otelnats's spans on and off in a running process, with no
// restart and no application code involved. Matches deployment option C —
// OTEL_NATS_TRACING_ENABLED is falsy; the relay enables and disables in both
// directions. The NATS business path still completes in every phase.
//
// There are no sleeps between the phases. The resolver caches nothing, so a
// rebound provider takes effect on the next operation; in production the only
// delay is the provider's poll interval, which is not in the library.
func TestRelayFlagTogglesNatsInstrumentation(t *testing.T) {
	rec := setupGlobalTracing(t)
	isolateFromRelay(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "false")

	// Phase 1: relay serves the flag as enabled — enables what env left off.
	setLibraryTracingFlag(t, true)

	nm := startNATS(t)
	handler := NewDemoTraceHandler(nm, nil)

	_ = postDemoTrace(t, handler)
	tracedOn := countNATSSpans(rec.Ended())
	if tracedOn == 0 {
		t.Fatalf("phase 1: no NATS spans while relay served otel-nats-tracing=enabled with "+
			"OTEL_NATS_TRACING_ENABLED=false; got %d spans overall", len(rec.Ended()))
	}

	// Phase 2: operator disables the flag. Same process, same connection.
	setLibraryTracingFlag(t, false)

	before := countNATSSpans(rec.Ended())
	_ = postDemoTrace(t, handler)
	if after := countNATSSpans(rec.Ended()); after != before {
		t.Errorf("phase 2: %d new NATS spans after relay set otel-nats-tracing=disabled, want 0 — "+
			"is the relay verdict still resolved per operation?", after-before)
	}

	// Phase 3: restore enabled; instrumentation must return via the relay.
	setLibraryTracingFlag(t, true)

	before = countNATSSpans(rec.Ended())
	_ = postDemoTrace(t, handler)
	if after := countNATSSpans(rec.Ended()); after == before {
		t.Errorf("phase 3: no new NATS spans after restoring otel-nats-tracing on the relay, want > 0")
	}
}

// TestRelayEnablesWhatEnvLeftOff pins the ladder direction that revoke-only
// models could not express: with OTEL_NATS_TRACING_ENABLED=false, a relay
// serving otel-nats-tracing=true MUST produce NATS spans.
//
// The named provider is bound before startNATS so RelayPossible() is true at
// construction and the instrumented implementation is allocated.
func TestRelayEnablesWhatEnvLeftOff(t *testing.T) {
	rec := setupGlobalTracing(t)
	isolateFromRelay(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "false")

	setLibraryTracingFlag(t, true)

	nm := startNATS(t)
	handler := NewDemoTraceHandler(nm, nil)

	got := postDemoTrace(t, handler)

	if !traceIDPattern.MatchString(got.TraceID) {
		t.Errorf("traceId = %q, want 32 hex chars", got.TraceID)
	}
	if n := countNATSSpans(rec.Ended()); n == 0 {
		t.Errorf("no NATS spans with OTEL_NATS_TRACING_ENABLED=false and relay enabled — "+
			"the relay must be able to enable what the environment left off (got %d spans overall)",
			len(rec.Ended()))
	}
}

// TestNoRelayEnvOffEmitsNoNATSSpans: with no relay possible and the module env
// falsy, the local ladder stays off and no NATS instrumentation spans appear.
// The business path still succeeds.
func TestNoRelayEnvOffEmitsNoNATSSpans(t *testing.T) {
	rec := setupGlobalTracing(t)
	isolateFromRelay(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "false")
	// Deliberately no setLibraryTracingFlag — RelayPossible stays false.

	nm := startNATS(t)
	handler := NewDemoTraceHandler(nm, nil)

	got := postDemoTrace(t, handler)

	if !traceIDPattern.MatchString(got.TraceID) {
		t.Errorf("traceId = %q, want 32 hex chars", got.TraceID)
	}
	if n := countNATSSpans(rec.Ended()); n != 0 {
		t.Errorf("%d NATS spans with no relay and OTEL_NATS_TRACING_ENABLED=false, want 0", n)
	}
}
