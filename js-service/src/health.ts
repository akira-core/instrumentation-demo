/**
 * The probe endpoint.
 *
 * `/healthz` reports healthy once the process is up and is deliberately BLIND to
 * both NATS and the feature-flag relay, exactly as the Go backend's is. That is
 * not merely defensive: if readiness depended on the relay, flipping
 * `otel-nats-tracing` could restart this pod — and a restart would destroy the
 * one thing the demo exists to prove, that the change reaches a RUNNING process.
 *
 * It reports NATS connectivity as a field for a human reading the response, and
 * never as part of the status code.
 */

import { createServer, type Server } from "node:http";

import type { Logger } from "./logger.js";

export interface HealthServer {
  close(): Promise<void>;
}

export function startHealthServer(
  port: number,
  natsConnected: () => boolean,
  logger: Logger,
): HealthServer {
  const server: Server = createServer((req, res) => {
    if (req.url === "/healthz") {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({ status: "ok", natsConnected: natsConnected() }));
      return;
    }
    res.writeHead(404, { "content-type": "application/json" });
    res.end(JSON.stringify({ status: "not found" }));
  });

  server.listen(port, () => {
    logger.info("health server listening", { port });
  });

  return {
    close: () =>
      new Promise<void>((resolve) => {
        server.close(() => resolve());
      }),
  };
}
