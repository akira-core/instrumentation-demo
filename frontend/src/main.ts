import "./style.css";

import { SpanStatusCode } from "@opentelemetry/api";

import { config } from "./config";
import { getTracer, initTracing } from "./tracing";

/** Shape of a successful POST /api/demo-trace response from demo-backend. */
interface DemoTraceResponse {
  readonly traceId: string;
  readonly spanId: string;
}

/** Runtime guard for the backend response -- never trust external data. */
function isDemoTraceResponse(value: unknown): value is DemoTraceResponse {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate.traceId === "string" &&
    typeof candidate.spanId === "string"
  );
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  return "Unexpected error";
}

/**
 * Builds a "View in Grafana" deep link for a trace ID.
 *
 * Grafana's exact deep-link URL shape (datasource UID, Explore pane JSON,
 * etc.) is not finalized yet -- that is built in a later, separate step.
 * This produces a placeholder query string of the form:
 *
 *   {VITE_GRAFANA_BASE_URL}/explore?traceID={traceId}
 *
 * so the demo has a working, clickable link today. Reconcile the path/query
 * shape here once the real Grafana deep-link format is decided.
 */
function buildGrafanaTraceUrl(traceId: string): string {
  const params = new URLSearchParams({ traceID: traceId });
  return `${config.grafanaBaseUrl}/explore?${params.toString()}`;
}

type AppElements = {
  readonly button: HTMLButtonElement;
  readonly status: HTMLParagraphElement;
  readonly result: HTMLDivElement;
};

function renderShell(root: HTMLElement): AppElements {
  root.replaceChildren();

  const main = document.createElement("main");
  main.className = "card";

  const heading = document.createElement("h1");
  heading.textContent = "Trace Propagation Demo";

  const description = document.createElement("p");
  description.className = "description";
  description.textContent =
    "Click the button to send a traced request from this browser to the " +
    "demo backend. The W3C traceparent header propagates the trace across " +
    "the frontend, backend, and the NATS request/reply round trip. " +
    "Library tracing (otel-nats-tracing) can be toggled live via the feature-flag ConfigMap.";

  const button = document.createElement("button");
  button.type = "button";
  button.textContent = "Start Trace";
  button.className = "start-button";

  const status = document.createElement("p");
  status.className = "status";
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");

  const result = document.createElement("div");
  result.className = "result";
  result.hidden = true;

  main.append(heading, description, button, status, result);
  root.append(main);

  return { button, status, result };
}

function setStatus(status: HTMLParagraphElement, message: string, tone: "idle" | "loading" | "error"): void {
  status.textContent = message;
  status.dataset.tone = tone;
}

function renderResult(result: HTMLDivElement, response: DemoTraceResponse): void {
  result.replaceChildren();
  result.hidden = false;

  const traceIdRow = document.createElement("div");
  traceIdRow.className = "result-row result-row--trace-id";
  const traceIdLabel = document.createElement("span");
  traceIdLabel.className = "result-label";
  traceIdLabel.textContent = "Trace ID";
  const traceIdValue = document.createElement("code");
  traceIdValue.className = "trace-id";
  traceIdValue.textContent = response.traceId;
  traceIdRow.append(traceIdLabel, traceIdValue);

  const spanIdRow = document.createElement("div");
  spanIdRow.className = "result-row";
  const spanIdLabel = document.createElement("span");
  spanIdLabel.className = "result-label";
  spanIdLabel.textContent = "Span ID";
  const spanIdValue = document.createElement("code");
  spanIdValue.textContent = response.spanId;
  spanIdRow.append(spanIdLabel, spanIdValue);

  const link = document.createElement("a");
  link.className = "grafana-link";
  link.href = buildGrafanaTraceUrl(response.traceId);
  link.target = "_blank";
  link.rel = "noopener noreferrer";
  link.textContent = "View in Grafana ↗";

  result.append(traceIdRow, spanIdRow, link);
}

function renderError(result: HTMLDivElement, message: string): void {
  result.replaceChildren();
  result.hidden = false;
  const errorBox = document.createElement("div");
  errorBox.className = "result-row result-row--error";
  errorBox.textContent = `Request failed: ${message}`;
  result.append(errorBox);
}

async function handleStartTrace(elements: AppElements): Promise<void> {
  const { button, status, result } = elements;

  button.disabled = true;
  result.hidden = true;
  setStatus(status, "Sending traced request to demo-backend...", "loading");

  const tracer = getTracer();
  await tracer.startActiveSpan("demo.start-trace-click", async (span) => {
    try {
      const response = await fetch(`${config.backendUrl}/api/demo-trace`, {
        method: "POST",
      });

      if (!response.ok) {
        throw new Error(`backend responded with HTTP ${response.status}`);
      }

      const payload: unknown = await response.json();
      if (!isDemoTraceResponse(payload)) {
        throw new Error("backend response did not match the expected shape");
      }

      span.setAttribute("demo.trace_id", payload.traceId);
      span.setAttribute("demo.span_id", payload.spanId);

      setStatus(status, "Trace completed.", "idle");
      renderResult(result, payload);
    } catch (error: unknown) {
      const message = getErrorMessage(error);
      span.recordException(error instanceof Error ? error : new Error(message));
      span.setStatus({ code: SpanStatusCode.ERROR, message });
      setStatus(status, "Something went wrong.", "error");
      renderError(result, message);
    } finally {
      span.end();
      button.disabled = false;
    }
  });
}

function main(): void {
  initTracing();

  const root = document.querySelector<HTMLDivElement>("#app");
  if (!root) {
    throw new Error('missing "#app" root element');
  }

  const elements = renderShell(root);
  setStatus(elements.status, "Ready.", "idle");
  elements.button.addEventListener("click", () => {
    void handleStartTrace(elements);
  });
}

main();
