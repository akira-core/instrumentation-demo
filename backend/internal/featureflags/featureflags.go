// Package featureflags wires this application to the GO Feature Flag relay
// proxy through OpenFeature so instrumentation libraries can resolve their
// own runtime flags (e.g. otelnats's otel-nats-tracing).
//
// Installing the provider here is deliberately an *application* concern. The
// instrumentation libraries never install one — the same rule that keeps them
// from initializing a TracerProvider — they only read from the process-global
// OpenFeature client. This package does not evaluate application-level flags;
// the demo request path is not gated by feature flags.
package featureflags

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	gofeatureflag "github.com/open-feature/go-sdk-contrib/providers/go-feature-flag/pkg"
	"github.com/open-feature/go-sdk/openfeature"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 30 * time.Second
)

// Setup installs the process-global OpenFeature provider pointing at the GOFF
// relay proxy, retrying in the background until it succeeds.
//
// It never blocks startup and never returns a fatal error: until the provider
// is installed, library evaluations fall back to their environment-variable
// defaults. That is exactly the documented degradation path, and it is what
// lets the backend be deployed before the relay proxy is ready without
// crash-looping.
func Setup(ctx context.Context, endpoint string, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	go installWithRetry(ctx, endpoint, logger)
}

func installWithRetry(ctx context.Context, endpoint string, logger *slog.Logger) {
	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}

		provider, err := gofeatureflag.NewProvider(gofeatureflag.ProviderOptions{
			Endpoint: endpoint,

			// REMOTE keeps library flag flips (via kubectl edit on the ConfigMap)
			// visible promptly once the library's own 1s snapshot cache expires.
			// An otelhttp-instrumented client is retained so any provider HTTP
			// traffic can join an active trace when one is present.
			EvaluationType: gofeatureflag.EvaluationTypeRemote,
			DisableCache:   true,

			HTTPClient: &http.Client{
				Timeout:   5 * time.Second,
				Transport: otelhttp.NewTransport(http.DefaultTransport),
			},

			Logger: logger,
		})
		if err == nil {
			if err = openfeature.SetProviderAndWait(provider); err == nil {
				logger.InfoContext(ctx, "featureflags: OpenFeature provider installed", "endpoint", endpoint)
				return
			}
		}

		logger.WarnContext(ctx, "featureflags: provider not ready, retrying; library flags fall back to env defaults",
			"error", err, "endpoint", endpoint, "backoff", backoff)
		if !sleepOrDone(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	if next := cur * 2; next <= maxBackoff {
		return next
	}
	return maxBackoff
}
