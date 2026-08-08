/**
 * The demo's JS NATS participant.
 *
 * It exists to prove one thing the Go backend alone cannot: that a single
 * `otel-nats-tracing` flip in the `demo-feature-flags` ConfigMap governs library
 * instrumentation in BOTH runtimes, on running processes, with no application
 * code and no restart.
 *
 * There is no flag-related code anywhere in this service. That is the point —
 * `@akira-core/otel-nats` installs its own OpenFeature provider from
 * `OTEL_INSTRUMENTATION_JS_FLAGS_ENDPOINT` and resolves the ladder per
 * operation. See deploy/base/js-service.yaml for the whole of the wiring.
 */

import { shutdown as shutdownFlags } from "@akira-core/otel-flags";

import { loadConfig } from "./config.js";
import { startHealthServer } from "./health.js";
import { logger } from "./logger.js";
import { startNatsFlow } from "./natsflow.js";
import { setupTelemetry } from "./telemetry.js";

const config = loadConfig();
const telemetry = setupTelemetry();

const shutdownSignal = new AbortController();
const nats = startNatsFlow(config, logger, shutdownSignal.signal);
const health = startHealthServer(config.port, () => nats.connected(), logger);

logger.info("js-service started", {
  natsUrl: config.natsUrl,
  requestSubject: config.requestSubject,
  jsSubject: config.jsSubject,
});

let shuttingDown = false;

async function shutdown(signal: string): Promise<void> {
  if (shuttingDown) {
    return;
  }
  shuttingDown = true;
  logger.info("shutting down", { signal });

  // Stop the connect-retry loop first, so a shutdown during backoff does not
  // establish a connection nobody will close.
  shutdownSignal.abort();
  await health.close();
  await nats.close();
  // Flush spans before the exporter goes away.
  await telemetry.shutdown();
  // The GO Feature Flag provider's poll timer is not unref'd, so without this the
  // process would keep running after everything else has stopped.
  await shutdownFlags();
}

for (const signal of ["SIGTERM", "SIGINT"] as const) {
  process.on(signal, () => {
    void shutdown(signal).then(
      () => process.exit(0),
      (err: unknown) => {
        logger.error("shutdown failed", { error: err instanceof Error ? err.message : String(err) });
        process.exit(1);
      },
    );
  });
}
