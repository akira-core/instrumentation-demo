## 1. VictoriaMetrics deployment

- [x] 1.1 Vendor the chart into `charts/victoria-metrics` (`helm repo add vm https://victoriametrics.github.io/helm-charts/ && helm pull vm/victoria-metrics-single --untar -d charts/`), stamping `charts/victoria-metrics/SOURCE.txt` with repo + exact chart/app version
- [x] 1.2 Write `deploy/values/victoria-metrics.yaml`: bounded `retentionPeriod` and persistent volume sized for `kind`, modest resources, ClusterIP service on 8428
- [x] 1.3 In the same values file enable the chart's built-in scraping (`server.scrape.enabled`) with an inline scrape config targeting NATS's Prometheus exporter, ClickHouse, Grafana, and the GOFF relay proxy's monitoring port — use in-cluster Service DNS names, not pod IPs
- [x] 1.4 Add `helm upgrade --install victoria-metrics charts/victoria-metrics -f deploy/values/victoria-metrics.yaml -n demo` to the Makefile's `helm-install` target
- [x] 1.5 Verify with `helm lint` + `helm template` before deploying

## 2. Expose metrics endpoints on existing components

- [x] 2.1 Enable the NATS chart's Prometheus exporter sidecar in `deploy/values/nats.yaml` (`promExporter.enabled`), keeping resources small
- [x] 2.2 Enable the GOFF relay proxy's monitoring port in `deploy/values/relay-proxy.yaml` (add `monitoringPort` to its config) — note this also switches the chart's liveness/readiness probes onto that port, so confirm the pod still becomes ready afterwards
- [x] 2.3 Confirm ClickHouse and Grafana expose Prometheus metrics with their current values (both do by default); record the exact path/port each uses for the scrape config in 1.3

## 3. rotel: per-signal exporter routing

> design.md Decision 2 flags this as the riskiest change in this set: it rewrites a
> working env-var layout, and a mistake drops traces silently rather than failing loudly.

- [x] 3.1 Confirm the exact named-exporter parameter spelling from `docker run --rm streamfold/rotel:v0.2.2 --help` (resolves design.md's open question) before editing the manifest
- [x] 3.2 Rewrite `deploy/base/rotel.yaml`'s env from the singular `ROTEL_EXPORTER=clickhouse` form to the named multi-exporter form: `ROTEL_EXPORTERS=ch:clickhouse,vm:otlp`, `ROTEL_EXPORTERS_TRACES=ch`, `ROTEL_EXPORTERS_METRICS=vm`, `ROTEL_EXPORTERS_INTERNAL_METRICS=vm`, with the ClickHouse parameters renamed accordingly and the ClickHouse credentials Secret still referenced (not inlined)
- [x] 3.3 Point the OTLP exporter at VictoriaMetrics: `ROTEL_EXPORTER_VM_ENDPOINT=http://victoria-metrics:8428/opentelemetry`, `ROTEL_EXPORTER_VM_PROTOCOL=http`
- [x] 3.4 **Trace regression check** — after rolling out the new rotel config, run a demo request and confirm its spans still land in ClickHouse and are still queryable by trace ID. A metrics-only check would pass while traces were silently broken.

## 4. Backend metrics pipeline

- [x] 4.1 Extend `backend/internal/telemetry` with a `MeterProvider` + periodic OTLP/HTTP metric exporter pointed at the existing `OTEL_EXPORTER_OTLP_ENDPOINT`, returning a shutdown func alongside the existing trace shutdown
- [x] 4.2 Register runtime instrumentation (`go.opentelemetry.io/contrib/instrumentation/runtime`) so useful metrics exist without hand-written counters
- [x] 4.3 Wrap the demo endpoint with `otelhttp` for server-side request metrics. **Spans deliberately disabled** via a noop TracerProvider: the handler already opens its own SERVER span from the extracted context, so letting otelhttp open one too would emit a second SERVER span per request and change the span shape the trace-propagation specs pin down. `/healthz` is left unwrapped so kubelet probes don't dominate the metrics.
- [x] 4.4 Confirm metrics-export failure cannot fail a request or block startup (unreachable collector must degrade silently)
- [x] 4.5 `go build`, `go vet`, `gofmt`, and `go test ./... -race` all clean; rebuild and `kind load` the backend image

## 5. Grafana metrics visualization

- [x] 5.1 Add a VictoriaMetrics data source to `deploy/values/grafana.yaml` using Grafana's built-in `prometheus` type (VM serves the Prometheus query API — no extra plugin needed), pointed at `http://victoria-metrics:8428`
- [x] 5.2 Provision a metrics dashboard alongside the existing trace dashboard, with panels covering at least: backend request rate/latency, rotel's own pipeline throughput (from `ROTEL_EXPORTERS_INTERNAL_METRICS`), NATS broker metrics, and relay-proxy evaluation activity
- [x] 5.3 Verify both dashboards and both data sources coexist after `helm upgrade` — the existing ClickHouse trace dashboard must keep working

## 6. otel-loadgen

- [x] 6.1 Write `deploy/loadgen/loadgen-job.yaml`: a Kubernetes `Job` running `streamfold/otel-loadgen` with `gen traces --otlp-endpoint rotel:4317`, pinned to an explicit image tag, with a bounded `--duration`, resource limits, and `restartPolicy: Never` + `backoffLimit: 0` so a failed run does not silently retry forever
- [x] 6.2 Keep `deploy/loadgen/` **out** of `deploy/base/`'s kustomization so `make deploy` never starts load
- [x] 6.3 Add `make load-test` with overridable variables (workers, spans-per-resource, push-interval, duration) that deletes any previous Job first — a completed Job's pod template is immutable, so a bare re-apply fails — then applies the Job
- [x] 6.4 Add `make load-test-clean` to delete the Job, and document both targets plus how to read the Job's logs in `README.md`

## 7. Verification

- [x] 7.1 Deploy the full stack and confirm every intended scrape target reports `up` in VictoriaMetrics (a wrong address fails as a missing target, not an error — check explicitly rather than assuming)
- [x] 7.2 Confirm backend metrics, rotel internal metrics, NATS, ClickHouse, Grafana, and relay-proxy metrics are all queryable from VictoriaMetrics
- [x] 7.3 Re-run the trace-propagation flow and confirm spans still reach ClickHouse and the existing trace dashboard still renders (regression guard for section 3)
- [x] 7.4 Run `make load-test`, confirm the Job runs to completion, its generated spans reach ClickHouse, and the load is visible in the metrics dashboard
- [x] 7.5 Re-run `make load-test` immediately afterwards to confirm repeat runs work, then `make load-test-clean`
- [x] 7.6 Confirm no workload entered CrashLoopBackOff during any of the above
- [x] 7.7 Run `openspec validate add-metrics-and-loadgen --strict` and fix anything reported
