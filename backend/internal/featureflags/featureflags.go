// Package featureflags wires this application to the GO Feature Flag relay
// proxy through OpenFeature, and evaluates the application-level flags that
// gate demo behavior.
//
// Installing the provider here is deliberately an *application* concern. The
// instrumentation libraries (otelnats et al.) never install one — the same rule
// that keeps them from initializing a TracerProvider — they only read from the
// process-global OpenFeature client. So this one Setup call is what lets an
// operator flip otelnats's own `otel-nats-tracing` flag on the relay and have it
// take effect live, with no application code involved in that decision.
package featureflags

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	gofeatureflag "github.com/open-feature/go-sdk-contrib/providers/go-feature-flag/pkg"
	"github.com/open-feature/go-sdk/openfeature"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/akira-core/instrumentation-demo/backend/featureflags"

const (
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 30 * time.Second
)

// Setup installs the process-global OpenFeature provider pointing at the GOFF
// relay proxy, retrying in the background until it succeeds.
//
// It never blocks startup and never returns a fatal error: until the provider
// is installed, every evaluation — this package's and otelnats's alike — falls
// back to the caller-supplied default, which for the instrumentation modules is
// their environment variable. That is exactly the documented degradation path,
// and it is what lets the backend be deployed before the relay proxy is ready
// without crash-looping.
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

			// REMOTE (not the INPROCESS default) so each evaluation is a real
			// call to the relay: that is what makes the flag-evaluation span
			// appear inside the demo trace, and what makes a `kubectl edit
			// configmap` visible on the very next request instead of after the
			// in-process ruleset's next refresh.
			EvaluationType: gofeatureflag.EvaluationTypeRemote,
			DisableCache:   true,

			// Instrumented transport, so the evaluation's HTTP call joins the
			// active trace as a child CLIENT span rather than vanishing.
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

		logger.WarnContext(ctx, "featureflags: provider not ready, retrying; evaluations fall back to defaults",
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

// Client evaluates application-level flags through OpenFeature.
type Client struct {
	client *openfeature.Client
	tracer trace.Tracer
}

// New returns a Client bound to the process-global OpenFeature provider. It is
// safe to construct before Setup's provider install completes: evaluations made
// in the meantime simply return their default.
func New() *Client {
	return &Client{
		client: openfeature.NewClient("demo-backend"),
		tracer: otel.Tracer(tracerName),
	}
}

// Enabled evaluates a boolean flag, wrapped in a span so the decision is
// visible in the trace alongside the HTTP call the provider makes to the relay.
// Every failure path inside OpenFeature resolves to def, so this never errors:
// an unreachable relay degrades to the supplied default rather than failing the
// request.
func (c *Client) Enabled(ctx context.Context, key string, def bool) bool {
	ctx, span := c.tracer.Start(ctx, "evaluate "+key, trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	value := c.client.Boolean(ctx, key, def, openfeature.EvaluationContext{})

	span.SetAttributes(
		attribute.String("feature_flag.key", key),
		attribute.Bool("feature_flag.result.value", value),
		attribute.String("feature_flag.provider.name", "GO Feature Flag"),
	)
	return value
}
