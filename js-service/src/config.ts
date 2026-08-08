/**
 * Everything this service reads from its environment.
 *
 * Deliberately does NOT read any `OTEL_*` flag variable. Those belong to
 * `@akira-core/otel-flags` and `@akira-core/otel-nats`, which read them
 * themselves — the whole point of the demo is that the flag wiring is zero-code,
 * so a config struct that mirrored those variables here would be the thing the
 * verification is trying to rule out.
 */

export interface Config {
  readonly natsUrl: string;
  readonly port: number;
  /** The subject the Go backend publishes its demo request on. */
  readonly requestSubject: string;
  /** This service's own subject, published to and consumed from. */
  readonly jsSubject: string;
}

const DEFAULT_PORT = 8081;

export function loadConfig(): Config {
  const rawPort = (process.env.PORT ?? "").trim();
  const port = rawPort === "" ? DEFAULT_PORT : Number(rawPort);
  if (!Number.isInteger(port) || port <= 0) {
    throw new Error(`PORT must be a positive integer, got ${JSON.stringify(rawPort)}`);
  }

  return {
    natsUrl: (process.env.NATS_URL ?? "").trim() || "nats://nats:4222",
    port,
    requestSubject: "demo.trace.request",
    jsSubject: "demo.trace.js",
  };
}
