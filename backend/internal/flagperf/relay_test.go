package flagperf

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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

// relayFlag is one flag in the configuration a GO Feature Flag relay proxy
// serves. The JSON shape is flag.InternalFlag from
// github.com/thomaspoignant/go-feature-flag/modules/core, of which only the two
// fields a static kill switch needs are spelled out here.
type relayFlag struct {
	Variations  map[string]any `json:"variations"`
	DefaultRule relayRule      `json:"defaultRule"`
}

type relayRule struct {
	Variation string `json:"variation"`
}

// boolFlag builds the flag definition the demo's ConfigMap carries, with the
// same two variation names an operator reads in deploy/base:
//
//	otel-nats-tracing:
//	  variations: {enabled: true, disabled: false}
//	  defaultRule: {variation: enabled}
func boolFlag(on bool) relayFlag {
	variation := "disabled"
	if on {
		variation = "enabled"
	}
	return relayFlag{
		Variations:  map[string]any{"enabled": true, "disabled": false},
		DefaultRule: relayRule{Variation: variation},
	}
}

// fakeRelay serves the one relay-proxy endpoint the in-process GO Feature Flag
// provider calls: POST /v1/flag/configuration.
//
// It exists so the benchmark can measure the path the demo actually deploys.
// Setting OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT is what makes otel-flags build
// and bind its own provider, and that auto-install is the only thing that
// latches autoInstalled — which in turn is what skips the per-evaluation
// context.WithTimeout and the three provider-registry reads that a test-installed
// provider pays on every single evaluation. A benchmark that installs an
// in-memory provider directly measures neither the deployment's cost nor its
// code path.
type fakeRelay struct {
	*httptest.Server
	etag string
}

// startFakeRelay serves flags until the test ends. natsTracing is the value the
// otel-nats module key resolves to; the master key is deliberately absent,
// exactly as in the demo's ConfigMap, so it resolves FLAG_NOT_FOUND and falls
// through to its local default — at full evaluation cost.
func startFakeRelay(t testing.TB, natsTracing bool) *fakeRelay {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"flags": map[string]relayFlag{
			flagKeyNATS:  boolFlag(natsTracing),
			readyFlagKey: boolFlag(true),
		},
	})
	if err != nil {
		t.Fatalf("marshal relay configuration: %v", err)
	}

	r := &fakeRelay{etag: fmt.Sprintf(`"flagperf-%t"`, natsTracing)}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/flag/configuration" || req.Method != http.MethodPost {
			http.NotFound(w, req)
			return
		}
		// The provider polls with If-None-Match after its first fetch. Answering
		// 304 is what a real relay does and keeps the polling goroutine off the
		// JSON-decode path while a measurement is running.
		if req.Header.Get("If-None-Match") == r.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", r.etag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(r.Server.Close)
	return r
}
