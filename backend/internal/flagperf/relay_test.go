package flagperf

import (
	"testing"

	"github.com/akira-core/instrumentation-demo/backend/internal/fakerelay"
)

// readyFlagKey is a sentinel flag the fake relay always serves as true while its
// local default is false.
//
// It exists because otel-flags installs the auto-install provider with the
// NON-blocking openfeature.SetNamedProvider, so a Connect returns before the
// provider's first configuration fetch has landed. Until it does, every key
// resolves to its local value — which for flag_off_relay is indistinguishable
// from the relay answering "disabled". Polling a key whose local and relay
// answers differ is what tells the two apart, so a measurement never starts
// against a provider that is still initialising.
const readyFlagKey = "otel-flagperf-ready"

// startFakeRelay serves the module flag at natsTracing until the test ends.
//
// The master key is deliberately absent, exactly as in the demo's ConfigMap, so
// it resolves FLAG_NOT_FOUND and falls through to its local default — at full
// evaluation cost, which is the point of measuring it.
//
// The relay itself lives in internal/fakerelay because the targeting suite
// needs the same fixture: two copies of a wire format that must match the
// provider's expectations is a drift waiting to happen.
func startFakeRelay(t testing.TB, natsTracing bool) *fakerelay.Server {
	t.Helper()
	srv, err := fakerelay.Start(map[string]fakerelay.Flag{
		flagKeyNATS:  fakerelay.Bool(natsTracing),
		readyFlagKey: fakerelay.Bool(true),
	})
	if err != nil {
		t.Fatalf("start fake relay: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}
