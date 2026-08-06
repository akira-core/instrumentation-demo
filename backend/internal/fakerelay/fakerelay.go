// Package fakerelay serves the one endpoint a GO Feature Flag in-process
// provider calls, so a test can exercise the flag path the demo actually
// deploys without running a relay-proxy container.
//
// Why this rather than an in-memory OpenFeature provider: setting
// OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT is what makes otel-flags build and
// bind its OWN provider, and only that auto-install supplies the targeting
// attributes (serviceName / service.name) and latches autoInstalled. A test
// that binds a memprovider directly therefore cannot reach targeting at all,
// and evaluates against a stub instead of GO Feature Flag's real rule engine —
// which is where a query is actually parsed, and where the difference between
// `serviceName` and `service.name` decides whether a rule matches anything.
//
// Why this rather than the relay-proxy container the otel-nats integration
// tests use: the rule engine runs IN PROCESS. The provider fetches
// configuration over HTTP at startup and on its poll, then evaluates locally,
// so serving that one configuration response is enough to get real evaluation
// at unit-test speed.
//
// It deliberately takes no *testing.T: callers own the returned server's
// lifetime, and a helper package that imports testing drags the flag
// registrations into anything that links it.
package fakerelay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
)

// Variation names match the vocabulary an operator reads in the demo's
// ConfigMap (deploy/base), so a flag defined here and a flag defined there look
// like the same thing.
const (
	variationEnabled  = "enabled"
	variationDisabled = "disabled"
)

// ConfigurationPath is the relay-proxy endpoint the in-process provider POSTs
// to for the flag configuration.
const ConfigurationPath = "/v1/flag/configuration"

// Rule is one targeting rule, or a flag's defaultRule.
//
// Query is a GO Feature Flag targeting query. The format is chosen by the
// engine from the query's own shape — a JSON object is read as JSONLogic and
// anything else as nikunjy — so `serviceName eq "demo-backend"` is nikunjy.
type Rule struct {
	Query     string `json:"query,omitempty"`
	Variation string `json:"variation,omitempty"`
}

// Flag mirrors the subset of flag.InternalFlag that a boolean switch needs.
type Flag struct {
	Variations  map[string]any `json:"variations"`
	Targeting   []Rule         `json:"targeting,omitempty"`
	DefaultRule Rule           `json:"defaultRule"`
}

func boolVariations() map[string]any {
	return map[string]any{variationEnabled: true, variationDisabled: false}
}

func variationFor(v bool) string {
	if v {
		return variationEnabled
	}
	return variationDisabled
}

// Bool is a flag with no targeting: every context gets v.
func Bool(v bool) Flag {
	return Flag{
		Variations:  boolVariations(),
		DefaultRule: Rule{Variation: variationFor(v)},
	}
}

// Targeted is a flag whose value depends on the evaluation context: contexts
// the query selects get whenMatch, everything else gets otherwise.
//
// This is the shape an operator writes to enable instrumentation for one
// service out of a fleet, and the shape nothing in this repository previously
// exercised.
func Targeted(query string, whenMatch, otherwise bool) Flag {
	return Flag{
		Variations:  boolVariations(),
		Targeting:   []Rule{{Query: query, Variation: variationFor(whenMatch)}},
		DefaultRule: Rule{Variation: variationFor(otherwise)},
	}
}

// Server is a running fake relay. Close it when the test ends.
type Server struct {
	*httptest.Server
	etag string
}

// Start serves flags until the returned server is closed. Its URL goes straight
// into OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT.
func Start(flags map[string]Flag) (*Server, error) {
	body, err := json.Marshal(map[string]any{"flags": flags})
	if err != nil {
		return nil, fmt.Errorf("fakerelay: marshal configuration: %w", err)
	}

	s := &Server{etag: fmt.Sprintf("%q", fmt.Sprintf("fakerelay-%d", len(body)))}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != ConfigurationPath || req.Method != http.MethodPost {
			http.NotFound(w, req)
			return
		}
		// The provider polls with If-None-Match after its first fetch. Answering
		// 304, as a real relay does, keeps the polling goroutine off the
		// JSON-decode path while a test is running.
		if req.Header.Get("If-None-Match") == s.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", s.etag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	return s, nil
}
