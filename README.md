# instrumentation-demo

Demo of W3C trace propagation across a full stack: a JS frontend calls a Go
backend over HTTP, the backend publishes to NATS and consumes the reply, and
a Kubernetes-native feature-flag relay proxy gates the flow — all visible as
one trace in Grafana, backed by `rotel` + ClickHouse. Everything runs in a
local `kind` cluster.

## Repo layout

- `frontend/` — browser app that starts a trace and calls the backend.
- `backend/` — Go HTTP service; continues the trace, evaluates a feature
  flag, publishes/consumes on NATS.
- `deploy/` — `kind` cluster config, Kustomize base for the in-house
  services, and Helm values for the vendored charts.
- `charts/` — vendored third-party Helm charts (NATS, ClickHouse, Grafana,
  VictoriaMetrics, and the GO Feature Flag relay proxy). Each subdirectory has
  a `SOURCE.txt` recording the exact chart version pulled.
- `deploy/loadgen/` — on-demand load-generator Job, deliberately outside the
  default deploy path.
- `third_party/` — git submodules for the sibling instrumentation repos
  (`instrumentation-js`, `instrumentation-go`) this demo consumes.
- `openspec/` — planning artifacts for this repo's changes.

## Getting the code

This repo uses git submodules for its cross-repo dependencies
(`@akira-core/otel-nats` from `instrumentation-js`, `otelnats`/
`oteljetstream` from `instrumentation-go`). Clone with submodules:

```sh
git clone --recurse-submodules <this-repo-url>
```

If you already cloned without `--recurse-submodules`, or need to pull in the
latest commit on a submodule's tracked branch:

```sh
make bootstrap
```

Each submodule tracks a branch (not a pinned commit) — see `.gitmodules`.
`instrumentation-go` tracks `main`. `instrumentation-js` tracks
`feat/otel-nats`, since `@akira-core/otel-nats` hasn't landed on `main` yet.
Advancing a submodule to the latest commit on its tracked branch is a
deliberate, explicit step — it never happens automatically on clone or pull:

```sh
git submodule update --remote third_party/instrumentation-go
git submodule update --remote third_party/instrumentation-js
```

To point a submodule at a different branch entirely:

```sh
git submodule set-branch --branch <branch> third_party/<repo>
git submodule update --remote third_party/<repo>
```

## Running the demo

Requires `docker`, `kind`, `helm`, and `kubectl` locally.

```sh
make deploy      # create the kind cluster, build+load images, install the
                  # vendored charts (NATS/ClickHouse/Grafana), apply the
                  # in-house services (frontend/backend/relay-proxy/rotel)
```

`make deploy` is safe to re-run — it upgrades in place rather than failing on
an already-existing cluster/release.

### Accessing the demo

Services are reachable via `kubectl port-forward` (no Ingress in this demo —
see `openspec/changes/demo-trace-propagation-stack/design.md` for why):

```sh
kubectl port-forward -n demo svc/frontend 8081:8080   # http://localhost:8081
kubectl port-forward -n demo svc/backend  8080:8080   # http://localhost:8080 (frontend calls this directly)
kubectl port-forward -n demo svc/grafana  3000:3000   # http://localhost:3000  (admin / demo-grafana-admin)
```

Run all three in separate terminals (or backgrounded), then open
`http://localhost:8081` and click **Start Trace**. The frontend's baked-in
defaults already point at `localhost:8080` (backend) and `localhost:4318`
(OTLP — port-forward `svc/rotel 4318:4318` too if you want the frontend's own
browser-side spans to reach Grafana, not just the backend/relay-proxy/NATS
spans), so no extra configuration is needed for the default port-forward
setup above.

### Feature flags

The GO Feature Flag relay proxy reads its flag definitions straight from the
`demo-feature-flags` ConfigMap through the Kubernetes API. Edit it live — the
relay proxy re-reads within a second and **nothing restarts**:

```sh
kubectl edit configmap demo-feature-flags -n demo
```

Two flags, demonstrating the two layers a relay proxy can drive:

| Flag | Consumed by | Effect when set to `disabled` |
|---|---|---|
| `otel-nats-tracing` | the `otelnats` **library** itself, no app code involved | NATS producer/consumer spans stop being emitted; the NATS round trip still runs |
| `demo-nats-flow` | **application** code, via the OpenFeature client | the backend skips the NATS round trip entirely (`natsFlowExecuted: false`) |

`otel-nats-tracing` is the interesting one: it is resolved per operation by the
instrumentation library, so flipping it turns instrumentation on and off in a
running process. Note the backend deliberately does **not** pass
`otelnats.WithTracingEnabled(...)` — that pins a connection static and no relay
change could reach it.

If the relay proxy is unreachable, evaluations fall back to the backend's
environment variables (`OTEL_NATS_TRACING_ENABLED`, and the in-code default for
`demo-nats-flow`), so the demo still works.

### Metrics

VictoriaMetrics (single node) collects metrics from the whole stack, two ways:

- **OTLP push** — the backend's application/runtime metrics and rotel's own
  internal pipeline telemetry, forwarded by rotel.
- **Prometheus scraping** — NATS, ClickHouse, Grafana and the relay proxy,
  which only expose `/metrics`.

Grafana has a provisioned **Ecosystem Metrics** dashboard alongside the trace
dashboard. Every panel's query was verified against live data.

```sh
kubectl port-forward -n demo svc/victoria-metrics 8428:8428   # http://localhost:8428
curl -s 'http://localhost:8428/api/v1/query?query=up'         # all scrape targets should report 1
```

### Performance testing

`otel-loadgen` (from the rotel authors) drives synthetic OTLP **trace** load at
rotel's gRPC endpoint. It never runs as part of `make deploy` — start it
explicitly, and it stops on its own:

```sh
make load-test                                              # defaults: 2 workers, 60s
make load-test WORKERS=10 SPANS_PER_RESOURCE=500 DURATION=2m
make load-test-logs
make load-test-clean
```

Watch its effect on the **Ecosystem Metrics** dashboard — the rotel panel shows
ingest throughput broken down by protocol (`grpc` is the load generator, `http`
is the demo's own services).

A measured run on a single-node `kind` cluster: 2 workers for 45s delivered
exactly 180,000 spans end-to-end (~4,000 spans/sec) with zero loss and no pod
restarts. Treat numbers like these as indicative of relative behavior on your
machine, not as benchmarks.

### A note on the NATS spans and trace IDs

A completed demo run produces **more than one trace ID**, by design. The
synchronous part (frontend → backend → flag evaluation → NATS publish) shares
the trace ID the frontend displays. The NATS *consumer* spans do not: `otelnats`
starts each `process <subject>` span as its own root trace and attaches the
publisher as an OTel **span link** rather than a parent — its documented,
intentional design for asynchronous messaging, and standard OTel guidance.

The provisioned Grafana dashboard reflects this: the timeline and span table
cover the entered trace ID, and a third panel ("Span-linked async spans")
follows the links to surface the NATS spans that live under their own trace IDs.

### Tearing down

```sh
make teardown     # deletes the kind cluster and everything in it
```
