// Command server is the demo-backend HTTP service: it serves
// POST /api/demo-trace and GET /healthz, exports traces via OTLP/HTTP, and
// drives the NATS demo round trip described in the repo's integration
// contract.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/akira-core/instrumentation-demo/backend/internal/config"
	"github.com/akira-core/instrumentation-demo/backend/internal/httpapi"
	"github.com/akira-core/instrumentation-demo/backend/internal/natsflow"
	"github.com/akira-core/instrumentation-demo/backend/internal/telemetry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := telemetry.Setup(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		logger.Error("failed to set up telemetry", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			logger.Error("telemetry shutdown error", "error", err)
		}
	}()

	// No OpenFeature setup here, deliberately. otelnats 0.8.0 installs its own
	// provider from OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT on the first
	// instrumented operation, bound to its private OpenFeature domain
	// (otel-instrumentation-go) with in-process evaluation and the data
	// collector disabled — the two settings an application-installed provider
	// has to get right and can silently get wrong. It never touches the DEFAULT
	// provider, so an application's own feature flags are unaffected.
	//
	// The trade-off this accepts: the auto-install is non-blocking, so between
	// process start and the provider's first fetch every flag reads as "no
	// opinion" and the environment alone decides. A revocation that was already
	// active when this process started is therefore missed for up to one poll
	// interval. Closing that window means installing a provider here with
	// openfeature.SetProviderAndWait before the first otelnats.Connect — worth
	// it for a service where a stray span is an incident, not for this demo.
	natsManager := natsflow.NewManager(cfg.NATSURL, logger)
	natsManager.Start(ctx)
	defer natsManager.Close()

	mux := httpapi.NewMux(cfg.CORSAllowedOrigin, natsManager, logger)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("http server shutdown error", "error", err)
		}
	}()

	logger.Info("demo-backend listening", "port", cfg.Port, "flagsEndpoint", cfg.FlagsEndpoint, "natsURL", cfg.NATSURL, "otlpEndpoint", cfg.OTLPEndpoint)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server error", "error", err)
		os.Exit(1)
	}
}
