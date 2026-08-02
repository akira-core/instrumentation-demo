/**
 * Browser OpenTelemetry setup for the trace-propagation demo.
 *
 * Registers a WebTracerProvider with:
 *  - a Resource identifying this service as "demo-frontend"
 *  - an OTLP/HTTP span exporter pointed at the configured collector endpoint
 *  - the Fetch auto-instrumentation, which injects W3C `traceparent` /
 *    `tracestate` headers into outgoing fetch() calls (same-origin always,
 *    cross-origin only for URLs matching `propagateTraceHeaderCorsUrls`)
 *  - ZoneContextManager so async context (the active span) survives across
 *    the microtask boundaries inside fetch()'s promise chain
 */
import type { Tracer } from "@opentelemetry/api";
import { ZoneContextManager } from "@opentelemetry/context-zone";
import {
  OTLPTraceExporter,
} from "@opentelemetry/exporter-trace-otlp-http";
import { registerInstrumentations } from "@opentelemetry/instrumentation";
import { FetchInstrumentation } from "@opentelemetry/instrumentation-fetch";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { ATTR_SERVICE_NAME } from "@opentelemetry/semantic-conventions";
import { BatchSpanProcessor, WebTracerProvider } from "@opentelemetry/sdk-trace-web";

import { config } from "./config";

const SERVICE_NAME = "demo-frontend";
const TRACER_NAME = "demo-frontend-app";

let tracer: Tracer | undefined;

/**
 * Initializes the WebTracerProvider, fetch instrumentation, and context
 * manager, and registers them as the global OTel implementations. Safe to
 * call once at app startup, before any traced fetch() calls are made.
 */
export function initTracing(): void {
  const exporter = new OTLPTraceExporter({
    url: `${config.otlpEndpoint}/v1/traces`,
  });

  const provider = new WebTracerProvider({
    resource: resourceFromAttributes({
      [ATTR_SERVICE_NAME]: SERVICE_NAME,
    }),
    spanProcessors: [new BatchSpanProcessor(exporter)],
  });

  provider.register({
    contextManager: new ZoneContextManager(),
  });

  registerInstrumentations({
    tracerProvider: provider,
    instrumentations: [
      new FetchInstrumentation({
        // Backend runs cross-origin from the frontend's dev/static host, so
        // the default same-origin-only propagation rule must be widened to
        // explicitly include the backend URL, per the OTel fetch
        // instrumentation docs.
        propagateTraceHeaderCorsUrls: [
          new RegExp(`^${escapeRegExp(config.backendUrl)}`),
        ],
        clearTimingResources: true,
      }),
    ],
  });

  tracer = provider.getTracer(TRACER_NAME);
}

/** Returns the demo app's tracer. Throws if called before initTracing(). */
export function getTracer(): Tracer {
  if (!tracer) {
    throw new Error("initTracing() must be called before getTracer()");
  }
  return tracer;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
