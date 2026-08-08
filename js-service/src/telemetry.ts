/**
 * OpenTelemetry bootstrap.
 *
 * The application's responsibility, exactly as in the Go backend: the
 * instrumentation packages never create a TracerProvider, they fall back to the
 * globally registered one.
 *
 * No sampler is configured. The Go backend does not configure one either, so
 * leaving it at the SDK default keeps the two services comparable — which is
 * what the evidence campaigns compare.
 */

import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-http";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { BatchSpanProcessor } from "@opentelemetry/sdk-trace-base";
import { NodeTracerProvider } from "@opentelemetry/sdk-trace-node";
import { ATTR_SERVICE_NAME } from "@opentelemetry/semantic-conventions";

export interface Telemetry {
  shutdown(): Promise<void>;
}

/**
 * Registers a TracerProvider globally and returns its shutdown hook.
 *
 * `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_SERVICE_NAME` are the standard
 * variables; the exporter appends the signal path itself.
 */
export function setupTelemetry(): Telemetry {
  const serviceName = (process.env.OTEL_SERVICE_NAME ?? "").trim() || "demo-js-service";

  const provider = new NodeTracerProvider({
    resource: resourceFromAttributes({ [ATTR_SERVICE_NAME]: serviceName }),
    spanProcessors: [new BatchSpanProcessor(new OTLPTraceExporter())],
  });
  // Registers the global TracerProvider AND the W3C propagators, which is what
  // the instrumentation packages fall back to.
  provider.register();

  return {
    shutdown: () => provider.shutdown(),
  };
}
