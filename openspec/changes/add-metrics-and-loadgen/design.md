## Context

The stack from `demo-trace-propagation-stack` is deployed and working: rotel receives OTLP traces and exports them to ClickHouse, Grafana reads ClickHouse, and a GOFF relay proxy drives dynamic flags. Nothing collects metrics, and the only source of traffic is a human clicking a button.

Two facts discovered while scoping this change shape the whole design, and both were verified against the running artifacts rather than assumed:

- **rotel supports per-signal exporter routing.** `rotel --help` (v0.2.2) shows `--exporters <name:type,...>` plus `--exporters-traces`, `--exporters-metrics` and `--exporters-internal-metrics`, with env equivalents `ROTEL_EXPORTERS`, `ROTEL_EXPORTERS_TRACES`, `ROTEL_EXPORTERS_METRICS`, `ROTEL_EXPORTERS_INTERNAL_METRICS`. Its OTLP receiver accepts metrics by default.
- **rotel has no Prometheus remote-write exporter.** Its exporter list is `otlp, blackhole, datadog, clickhouse, awsxray, awsemf, kafka, file`. So metrics reach VictoriaMetrics over **OTLP**, not remote write.
- **otel-loadgen generates traces only** — not metrics or logs, despite the name suggesting general OTel load. Its flags are `--otlp-endpoint`, `--workers`, `--spans-per-resource`, `--push-interval`, `--duration`, `--http`.

See proposal.md for motivation and scope.

## Goals / Non-Goals

**Goals:**
- One metrics store holding every component's metrics, reachable from Grafana with no manual setup.
- Keep the existing trace path byte-for-byte behaviorally identical — this change must not become a trace-pipeline regression.
- Load testing that is opt-in, bounded, and repeatable.

**Non-Goals:**
- Logs. VictoriaLogs exists and would slot in the same way, but nothing in the stack currently emits structured logs worth centralizing.
- Alerting/recording rules (`vmalert`), long-term retention, or HA — single node, short retention, ephemeral cluster.
- Load-testing the metrics path itself: otel-loadgen emits traces, so it loads the trace pipeline; metrics are how that load is *observed*, not what is generated.
- Tuning or benchmarking conclusions. This change makes performance testing *possible*; interpreting results is separate work.

## Decisions

### 1. VictoriaMetrics single-node, ingesting both by OTLP push and by scraping
Vendor `vm/victoria-metrics-single` (chart 0.43.0, app v1.148.0) into `charts/victoria-metrics`. It is the single metrics store, fed two ways because the ecosystem speaks two protocols:

- **OTLP push** for anything already emitting OTel metrics (the backend, and rotel's own internal telemetry), forwarded by rotel. VictoriaMetrics accepts protobuf OTLP at `/opentelemetry/v1/metrics` on its HTTP port (8428) with no extra flags, so rotel's OTLP exporter points at base endpoint `http://victoria-metrics:8428/opentelemetry` and the exporter appends `/v1/metrics`.
- **Prometheus scraping** for components that only expose `/metrics` and will never speak OTLP: NATS (via the chart's `promExporter` sidecar), ClickHouse, Grafana, and the GOFF relay proxy (via its `monitoringPort`). The chart exposes `server.scrape.enabled` + inline scrape config, so this needs no separate vmagent deployment.
- **Alternative considered**: run `vmagent` for scraping and keep VM as pure storage — the canonical production split, but a second workload and a second config surface for a demo where single-node VM already scrapes perfectly well.
- **Alternative considered**: Prometheus itself instead of VictoriaMetrics — rejected, the user asked for VictoriaMetrics specifically, and VM's single binary with built-in scraping is a better fit for `kind` than Prometheus + separate exporters.

### 2. rotel gains per-signal routing; traces keep their existing destination
The current manifest configures exactly one exporter via the singular `ROTEL_EXPORTER=clickhouse` plus `ROTEL_CLICKHOUSE_EXPORTER_*` parameters. Metrics require moving to rotel's **named multi-exporter** form, which renames those variables:

```
ROTEL_EXPORTERS=ch:clickhouse,vm:otlp
ROTEL_EXPORTERS_TRACES=ch
ROTEL_EXPORTERS_METRICS=vm
ROTEL_EXPORTERS_INTERNAL_METRICS=vm
ROTEL_EXPORTER_CH_<param>=...      # was ROTEL_CLICKHOUSE_EXPORTER_<param>
ROTEL_EXPORTER_VM_ENDPOINT=http://victoria-metrics:8428/opentelemetry
ROTEL_EXPORTER_VM_PROTOCOL=http
```

`ROTEL_EXPORTERS_INTERNAL_METRICS=vm` is what makes the collector's own pipeline health observable — the thing you most want during a load test, and the reason rotel not having a scrape endpoint doesn't matter.

**This rewrite is the single riskiest edit in the change**: it replaces a working, live-verified configuration, and a wrong variable name fails by silently dropping traces rather than by refusing to start. So the task list verifies the trace path end-to-end *after* the rewrite, not just that metrics appeared — a metrics-only check would pass while traces were quietly broken.
- **Alternative considered**: a second rotel deployment dedicated to metrics, leaving the working traces deployment untouched. Zero regression risk, but two collectors to explain and configure for one demo, and it would not carry rotel's internal telemetry from the traces instance.

### 3. Backend gains a metrics pipeline; the trace pipeline is untouched
`backend/internal/telemetry` currently builds a `TracerProvider` only. It gains a `MeterProvider` with a periodic OTLP/HTTP metrics exporter pointed at the same rotel endpoint, plus runtime instrumentation (`otel/contrib/instrumentation/runtime`) so there is meaningful data without hand-instrumenting counters. HTTP server metrics come free from the `otelhttp` handler already in the dependency set. Metrics-export failure must never surface to a request — the SDK's exporter already swallows and retries, so this is a matter of not wiring the exporter's error into startup.

### 4. otel-loadgen as an on-demand Job, excluded from the default apply
`streamfold/otel-loadgen` (same authors as rotel) ships a container image and generates trace load with `gen traces --otlp-endpoint <host:port> --workers N --spans-per-resource N --push-interval D --duration D`. It is deployed as a Kubernetes **Job** — the right primitive for bounded, run-to-completion work — living in `deploy/loadgen/` and deliberately **outside** `deploy/base/`'s kustomization, so `make deploy` never starts load. A `make load-test` target applies it with overridable variables and `make load-test-clean` deletes it.

Two details that make repeat runs work: the Job is deleted before being re-applied (a completed Job's pod template is immutable, so a bare re-apply fails), and it targets rotel's **gRPC** port 4317, which is otel-loadgen's default protocol and already exposed on rotel's Service.
- **Alternative considered**: a long-running Deployment with a replica count as the intensity knob — but "load that runs until someone remembers to turn it off" is a worse default in a demo cluster than a Job with a duration.
- **Alternative considered**: driving load through the backend's real `POST /api/demo-trace` (via `hey`/`k6`) rather than synthetic spans at the collector. That would exercise the whole application path including NATS and flag evaluation, which is a genuinely different and also-useful test — but it is not what the user asked for, and it stresses the demo app rather than the telemetry pipeline. Noted as a natural follow-up.

### 5. Grafana gets a second data source, not a second Grafana
The existing Grafana release adds a `victoriametrics-datasource`-compatible Prometheus data source (VictoriaMetrics speaks the Prometheus query API, so the built-in `prometheus` type works with no extra plugin) plus a provisioned metrics dashboard, alongside the existing ClickHouse trace data source and dashboard. Both are provisioned through the same `deploy/values/grafana.yaml` mechanism already in use.

## Risks / Trade-offs

- [Rewriting rotel's exporter env vars can silently break the working trace pipeline] → Verify traces end-to-end after the change, not just metrics; the task list makes this an explicit, separate check. If the named-exporter parameter naming turns out to differ from `ROTEL_EXPORTER_{NAME}_{PARAM}`, fall back to Decision 2's alternative (a second rotel instance) rather than leaving traces degraded.
- [VictoriaMetrics scraping needs correct in-cluster target addresses, and a wrong one fails silently as a missing target rather than an error] → Verify each intended target actually appears as `up` in VM, rather than assuming the scrape config is right because VM started.
- [A load test on a single-node `kind` cluster can starve the other workloads, making the metrics it produces measure the laptop rather than the stack] → Conservative defaults, an explicit `--duration`, and resource limits on the Job; document that results are indicative, not benchmarks.
- [otel-loadgen generates traces only, so "performance testing" here does not cover the metrics path] → Stated as a non-goal; the metrics path's own load is the byproduct of the trace load, which is the realistic coupling anyway.
- [More components in a single-node cluster increases the chance of scheduling pressure] → VictoriaMetrics single-node with short retention and small resource requests; the NATS Prometheus exporter is a lightweight sidecar.

## Migration Plan

Additive. Rollback is `helm uninstall victoria-metrics`, reverting `deploy/base/rotel.yaml` to its single-exporter form, and deleting the loadgen Job — no persistent state outside the ephemeral cluster.

## Open Questions

(resolved during implementation — recorded below rather than deleted, since each one was a silent-failure mode that cost real debugging time)

- **rotel's named-exporter parameter spelling.** Confirmed as `ROTEL_EXPORTER_{NAME}_{PARAM}` (e.g. `ROTEL_EXPORTER_CH_ENDPOINT`), replacing the type-based `ROTEL_CLICKHOUSE_EXPORTER_*` form used with a single exporter. Verified by running the image with the exact env layout before touching the live manifest.
- **`ROTEL_EXPORTERS_INTERNAL_METRICS` alone does nothing.** Routing internal telemetry is not the same as enabling it: `ROTEL_ENABLE_INTERNAL_TELEMETRY=true` is also required. With only the routing set, rotel starts cleanly and emits no internal metrics at all, with no warning.
- **VictoriaMetrics' Service name.** The chart's fullname template yields `victoria-metrics-victoria-metrics-single-server`, not `victoria-metrics`. rotel's exporter failed with a bare "unable to connect" until `server.fullnameOverride: victoria-metrics` was set. Both rotel and Grafana address it by the short name.
- **OTLP metrics keep OTel dotted names.** VictoriaMetrics stores `http.server.request.duration` verbatim unless `-opentelemetry.usePrometheusNaming=true` is set, so OTLP-pushed metrics are invisible to ordinary PromQL and look nothing like the scraped metrics beside them. The flag is now set, giving one consistent namespace across both ingestion paths.
- **The NATS chart exposes no Service port for its Prometheus exporter.** The sidecar serves 7777 inside the pod but no Service maps it, so the target sat permanently down. Fixed with `service.merge`, which **replaces** `spec.ports` rather than appending — the client port 4222 has to be restated or every NATS client loses its connection. (Caught by `helm template` before rollout.)
- **rotel's span counters are per-protocol.** `rotel_receiver_accepted_spans_total` carries a `protocol` label; the loadgen (gRPC) and the demo services (HTTP) land on separate series, so any dashboard panel must keep that label rather than aggregating it away.
