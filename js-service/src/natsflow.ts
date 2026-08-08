/**
 * The NATS lifecycle and this service's part of the demo flow.
 *
 * Topology, and why it is additive:
 *
 *   Go backend --publish--> demo.trace.request --+--> Go subscriber --> demo.trace.reply
 *                                                |
 *                                                +--> THIS service --> demo.trace.js --> THIS service
 *
 * The subscription on the request subject carries NO queue group. Core NATS
 * delivers to every distinct subscription and queue groups only load-balance
 * within a group, so an ungrouped subscriber here receives every message the Go
 * subscriber also receives — nothing is stolen, and the Go round trip's span
 * counts are unchanged. Joining its queue group instead would take roughly half
 * its messages and invalidate every published campaign.
 *
 * The second hop is a self-loop: this service publishes on its own subject and
 * consumes it. That is artificial, and it buys coverage of the PRODUCING side of
 * the library — header injection and a producer span — without a second image.
 */

import { connect, type Conn, type OtelMsg } from "@akira-core/otel-nats";
import type { Logger } from "./logger.js";
import type { Config } from "./config.js";

const CORRELATION_HEADER = "X-Demo-Correlation-Id";

const INITIAL_BACKOFF_MS = 500;
const MAX_BACKOFF_MS = 30_000;

export interface NatsFlow {
  connected(): boolean;
  close(): Promise<void>;
}

/**
 * Starts connecting in the background and returns immediately.
 *
 * Never blocks startup and never throws for an unreachable server: a
 * not-yet-ready NATS must not crash-loop this pod, and /healthz must not depend
 * on it. Mirrors the Go backend's `natsflow.Start`.
 */
export function startNatsFlow(config: Config, logger: Logger, signal: AbortSignal): NatsFlow {
  let conn: Conn | undefined;

  const flow: NatsFlow = {
    connected: () => conn !== undefined,
    close: async () => {
      const current = conn;
      conn = undefined;
      if (current) {
        await current.close();
      }
    },
  };

  void connectLoop();
  return flow;

  async function connectLoop(): Promise<void> {
    let backoff = INITIAL_BACKOFF_MS;

    while (!signal.aborted) {
      try {
        // Deliberately NO tracingEnabled option. Under the library ladder
        // (relay > env > option > default) the option is a valid local rung, but
        // this demo leaves it unset so what is verified is explained only by the
        // deploy environment and the relay ConfigMap flag.
        const established = await connect({ servers: config.natsUrl });
        subscribe(established);
        conn = established;
        logger.info("connected to nats", { url: config.natsUrl });
        return;
      } catch (err) {
        logger.warn("nats connect failed, retrying", {
          error: describe(err),
          url: config.natsUrl,
          backoffMs: backoff,
        });
        if (!(await sleep(backoff, signal))) {
          return;
        }
        backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
      }
    }
  }

  function subscribe(established: Conn): void {
    // No queue group: see the module comment.
    established.subscribe(config.requestSubject, (msg) => {
      handleRequest(established, msg);
    });
    established.subscribe(config.jsSubject, (msg) => {
      handleJsMessage(msg);
    });
  }

  /**
   * The consumer side of the Go backend's publish: a CONSUMER span linked back to
   * the producer, then a publish that continues this span's context.
   *
   * That context is a NEW trace, not the Go request's. The library starts a
   * consumer span as a root and attaches the producer as an OTel span LINK rather
   * than as a parent, so a completed demo run produces several trace IDs by
   * design — the same property the Go path already documents.
   */
  function handleRequest(established: Conn, { msg }: OtelMsg): void {
    const correlationId = msg.headers?.get(CORRELATION_HEADER) ?? "";
    // Logged, not forwarded: nothing correlates on the js subject, and the trace
    // context that DOES need to travel is injected by the library itself.
    logger.info("processed demo.trace.request", { correlationId });

    try {
      // Runs inside the handler, where the library has already made the consumer
      // span active, so this producer span lands on the consumer's trace and its
      // headers carry that context onward.
      established.publish(config.jsSubject, "ok");
    } catch (err) {
      logger.error("failed to publish on the js subject", { error: describe(err) });
    }
  }

  function handleJsMessage({ msg }: OtelMsg): void {
    logger.info("processed demo.trace.js", { subject: msg.subject });
  }
}

/** Resolves true when the delay elapsed, false when the signal aborted first. */
function sleep(ms: number, signal: AbortSignal): Promise<boolean> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve(false);
      return;
    }
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve(true);
    }, ms);
    const onAbort = (): void => {
      clearTimeout(timer);
      resolve(false);
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function describe(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
