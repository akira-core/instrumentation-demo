## Why

The demo stack currently observes only **traces**: rotel receives OTLP traces and exports them to ClickHouse for Grafana. Nothing collects metrics, so there is no way to see how the ecosystem actually behaves — NATS throughput, ClickHouse ingestion, relay-proxy evaluation rate, rotel's own pipeline health, or the backend's request latency. That gap matters most exactly when the system is under load, and there is currently no way to put it under load either: every trace comes from a human clicking "Start Trace" in the browser, one at a time.

Adding a metrics backend and a load generator turns the demo from a single-request walkthrough into something that can be pushed hard and measured, which is what makes the instrumentation libraries' overhead and the pipeline's limits observable.

## What Changes

- Deploy **VictoriaMetrics** in single-node mode as the metrics store for the whole ecosystem, ingesting from two directions: OTLP metrics forwarded by rotel, and Prometheus-style scraping of the components that only expose a `/metrics` endpoint.
- Extend **rotel** from one exporter to per-signal routing: traces continue to ClickHouse (unchanged), while metrics — including rotel's own internal telemetry — are exported to VictoriaMetrics.
- Add an OTel **metrics pipeline to the backend**, which today configures traces only, so application-level metrics exist to collect.
- Enable the metrics endpoints already available on deployed components: the NATS chart's Prometheus exporter and the GOFF relay proxy's monitoring port.
- Provision a **Grafana metrics data source and dashboard** against VictoriaMetrics, alongside the existing trace dashboard.
- Add **otel-loadgen** (`streamfold/otel-loadgen`, from the same authors as rotel) as an on-demand Kubernetes Job for driving synthetic OTLP trace load at the pipeline, with configurable worker count, spans-per-resource, push interval and duration, plus a `make` target to run and tear down a load test.

## Capabilities

### New Capabilities
- `metrics-pipeline`: Behavior of metrics collection across the stack — what VictoriaMetrics ingests via OTLP and via scraping, which components must expose metrics, and what the provisioned Grafana metrics dashboard must show.
- `load-generation`: Behavior of the on-demand load generator — how a developer starts a load test with a chosen intensity and duration, how it targets the pipeline, and how its effect is observed and cleaned up.

### Modified Capabilities
(none — `observability-stack` from the `demo-trace-propagation-stack` change is not yet archived into `openspec/specs/`, so there is no main spec to write a delta against. The collector's widened contract — accepting OTLP metrics and routing them separately from traces — is therefore specified inside `metrics-pipeline`, including an explicit requirement that the existing trace path keeps working unchanged.)

## Impact

- **New infra**: `charts/victoria-metrics` (vendored `vm/victoria-metrics-single`) with `deploy/values/victoria-metrics.yaml`; a load-generator Job manifest kept out of the default apply path so it only runs when asked.
- **Modified infra**: `deploy/base/rotel.yaml` (per-signal exporter routing — the riskiest edit here, since it rewrites an env-var layout that is currently working), `deploy/values/nats.yaml` (Prometheus exporter), `deploy/values/relay-proxy.yaml` (monitoring port), `deploy/values/grafana.yaml` (second data source + metrics dashboard), `Makefile` (chart install + load-test targets), `README.md`.
- **Modified code**: `backend/internal/telemetry` gains a metrics provider and exporter; no change to the trace path, the NATS flow, or the feature-flag wiring.
- **No impact** on the frontend, on `third_party/` submodules, or on the trace-propagation behavior verified by the previous change — traces must keep flowing to ClickHouse exactly as before, and that is a regression risk this change must explicitly check.
