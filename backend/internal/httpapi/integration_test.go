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
func TestDemoTraceHandler_FullNatsRoundTrip(t *testing.T) {
	_ = setupGlobalTracing(t)
	isolateFromRelay(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "1")

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
// relay proxy turns otelnats's spans on and off in a running process, with no
// restart and no application code involved in the decision. The NATS business
// path still completes in every phase.
//
// Both environment tiers are ON here, which is what the deployment sets. Under
// 0.8.0's revoke-only model that is mandatory rather than incidental: the relay
// can only subtract, so a module whose environment variable is off can never be
// switched back on by any flag value — see
// TestRelayCannotEnableNatsInstrumentation, which pins that half.
//
// There are no sleeps between the phases. The resolver caches nothing, so a
// rebound provider takes effect on the next operation; in production the only
// delay is the provider's poll interval, which is not in the library.
func TestRelayFlagTogglesNatsInstrumentation(t *testing.T) {
	rec := setupGlobalTracing(t)
	isolateFromRelay(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "1")

	// Phase 1: relay serves the flag as enabled.
	setLibraryTracingFlag(t, true)

	nm := startNATS(t)
	handler := NewDemoTraceHandler(nm, nil)

	_ = postDemoTrace(t, handler)
	tracedOn := countNATSSpans(rec.Ended())
	if tracedOn == 0 {
		t.Fatalf("phase 1: no NATS spans emitted while the relay served otel-nats-tracing=enabled; "+
			"got %d spans overall", len(rec.Ended()))
	}

	// Phase 2: operator revokes the flag on the relay. Same process, same
	// connection, no restart. Business path must still succeed.
	setLibraryTracingFlag(t, false)

	before := countNATSSpans(rec.Ended())
	_ = postDemoTrace(t, handler)
	if after := countNATSSpans(rec.Ended()); after != before {
		t.Errorf("phase 2: %d new NATS spans emitted after the relay revoked otel-nats-tracing, want 0 — "+
			"is the relay verdict still resolved per operation?", after-before)
	}

	// Phase 3: restore the flag; instrumentation must return. This is the half
	// that only holds because the environment tiers are on — restoring a flag
	// lifts a revocation, it does not enable anything.
	setLibraryTracingFlag(t, true)

	before = countNATSSpans(rec.Ended())
	_ = postDemoTrace(t, handler)
	if after := countNATSSpans(rec.Ended()); after == before {
		t.Errorf("phase 3: no new NATS spans after restoring otel-nats-tracing on the relay, want > 0")
	}
}

// TestRelayCannotEnableNatsInstrumentation pins the other half of 0.8.0's
// kill-switch model, and the half that is easy to regress without noticing: the
// relay can only REVOKE. With OTEL_NATS_TRACING_ENABLED off in the environment,
// a relay serving otel-nats-tracing=true must not produce a single span.
//
// Before 0.8.0 the module env var was passed as the evaluation DEFAULT, so a
// relay value overrode it in both directions and this test's setup was the
// documented way to turn instrumentation ON. It is now the documented way to
// keep it off, which is why this is worth a test rather than a comment: the two
// models differ only in behavior, not in configuration.
//
// The module variable is read once, at construction, so it is set before
// startNATS builds the connection.
func TestRelayCannotEnableNatsInstrumentation(t *testing.T) {
	rec := setupGlobalTracing(t)
	isolateFromRelay(t)
	t.Setenv(envGlobalTracingEnabled, "1")
	t.Setenv(envNATSTracingEnabled, "false")

	setLibraryTracingFlag(t, true)

	nm := startNATS(t)
	handler := NewDemoTraceHandler(nm, nil)

	got := postDemoTrace(t, handler)

	// The business path still runs — the flag governs telemetry, not behavior.
	if !traceIDPattern.MatchString(got.TraceID) {
		t.Errorf("traceId = %q, want 32 hex chars", got.TraceID)
	}
	if n := countNATSSpans(rec.Ended()); n != 0 {
		t.Errorf("%d NATS spans emitted with OTEL_NATS_TRACING_ENABLED=false, want 0 — "+
			"the relay must not be able to enable what the environment left off", n)
	}
}
