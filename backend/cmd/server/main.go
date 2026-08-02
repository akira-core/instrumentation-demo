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
	"github.com/akira-core/instrumentation-demo/backend/internal/featureflags"
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

	// Install the OpenFeature provider so otelnats can resolve otel-nats-tracing
	// at runtime. The library reads the process-global client but never installs
	// a provider itself. Application request handlers do not evaluate flags.
	featureflags.Setup(ctx, cfg.RelayProxyURL, logger)

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

	logger.Info("demo-backend listening", "port", cfg.Port, "relayProxyURL", cfg.RelayProxyURL, "natsURL", cfg.NATSURL, "otlpEndpoint", cfg.OTLPEndpoint)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server error", "error", err)
		os.Exit(1)
	}
}
