# instrumentation-demo

End-to-end demo of **W3C trace propagation** and the sibling instrumentation
libraries (`instrumentation-go` / `instrumentation-js`) running in a local
`kind` cluster.

A browser frontend starts a trace, calls a Go backend over HTTP, the backend
publishes and consumes on NATS via `otelnats`, and the whole path is exported
through `rotel` → ClickHouse and inspected in Grafana — including metrics via
VictoriaMetrics. A GOFF relay proxy serves **library** feature flags
(e.g. `otel-nats-tracing`) so you can prove dynamic instrumentation toggles.

**Languages:** [繁體中文（README.zh-TW.md）](README.zh-TW.md)

---

## What this demo is for

Use this stack to **check that our instrumentation libraries work in a
realistic path**, not only in unit tests:

| Library / package | Role in this demo |
|---|---|
| **`otelnats`** (`instrumentation-go`) | NATS publish/subscribe spans, W3C headers on messages, async **span links**, runtime flag `otel-nats-tracing` |
| **OpenFeature + GOFF provider** (app installs provider only) | Lets `otelnats` resolve `otel-nats-tracing` at runtime without restarting |
| **Browser OTel** (`@opentelemetry/sdk-trace-web` + fetch instrumentation) | CLIENT span + `traceparent` injection on `POST /api/demo-trace` |
| **`@akira-core/otel-nats`** (`instrumentation-js`, submodule) | Available in-tree for JS NATS work; this UI demo focuses on the HTTP→Go→NATS path |

If spans appear correctly in Grafana after a click, and flags turn library
behavior on/off live, the libraries are wired the way production apps should
wire them.

---

## Architecture

### High-level components

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  kind cluster  (namespace: demo)                                            │
│                                                                             │
│  Browser ──port-forward──► Frontend (nginx + static JS)                     │
│       │                         │                                           │
│       │  POST /api/demo-trace   │  OTLP/HTTP (browser spans)                │
│       │  + traceparent          ▼                                           │
│       └──────────────────► Backend (Go) ──provider──► Relay proxy (GOFF)    │
│                                  │                    ▲                     │
│                                  │ otelnats           │ ConfigMap           │
│                                  ▼   (library flags)  │ demo-feature-flags  │
│                               NATS                    │ (otel-nats-tracing) │
│                                  │                                          │
│  Backend + Frontend + Relay ──► rotel ──► ClickHouse ◄── Grafana           │
│  (OTLP)                         │                                           │
│                                  └──► VictoriaMetrics ◄── Grafana (metrics) │
│                                       (also scrapes NATS/CH/Grafana/relay)  │
└─────────────────────────────────────────────────────────────────────────────┘
```

| Component | Image / source | Responsibility |
|---|---|---|
| **frontend** | `frontend/` | Starts CLIENT span, injects W3C headers, shows result + Grafana link |
| **backend** | `backend/` | SERVER span, installs OpenFeature provider, NATS request/reply via `otelnats` |
| **NATS** | vendored chart | Message bus for the demo round trip |
| **relay-proxy** | GOFF chart | Serves library flag `otel-nats-tracing` from a live ConfigMap |
| **rotel** | deploy manifest | OTLP collector → ClickHouse (+ pipeline metrics) |
| **ClickHouse** | vendored chart | Trace storage |
| **Grafana** | vendored chart | Trace + metrics dashboards |
| **VictoriaMetrics** | vendored chart | Metrics store (OTLP push + Prometheus scrape) |

### Request flow (one click on **Start Trace**)

1. **Frontend** — WebTracerProvider + Fetch instrumentation create a CLIENT
   span and inject `traceparent` / `tracestate` on
   `POST /api/demo-trace`.
2. **Backend HTTP** — extracts context, starts a SERVER span
   (`POST /api/demo-trace`). Same **trace ID** as the browser when
   port-forwards and CORS propagation are correct.
3. **NATS publish** — always runs on a successful path; `otelnats` PRODUCER
   span when library tracing is on; W3C context in message headers.
   Subject: `demo.trace.request`.
4. **NATS consume + reply** — in-process subscriber (same binary, simulating
   a downstream consumer) uses `otelnats` CONSUMER span on
   `process demo.trace.request`, then publishes a reply on
   `demo.trace.reply`.
5. **Response** — JSON `{ traceId, spanId }` so you can paste the ID into
   Grafana.

### Important: NATS spans use span links (multiple trace IDs)

A completed run produces **more than one trace ID by design**. The
synchronous path (frontend → backend → NATS publish)
shares the ID shown in the UI. NATS **consumer** spans are separate roots:
`otelnats` attaches the publisher as an OTel **span link**, not a parent —
intentional for async messaging (OTel messaging guidance).

The provisioned Grafana dashboard has a panel for **span-linked async
spans** so you can still see the full story.

### Telemetry pipeline

```
Services (OTLP/HTTP or gRPC)
        │
        ▼
     rotel  ──batch──►  ClickHouse (otel_traces)  ──query──►  Grafana (traces)
        │
        └── OTLP metrics ──► VictoriaMetrics ──query──► Grafana (Ecosystem Metrics)
```

Prometheus scrape targets (NATS, ClickHouse, Grafana, relay) also land in
VictoriaMetrics.

### Feature flags (library only)

| Flag | Consumed by | When `disabled` |
|---|---|---|
| `otel-nats-tracing` | **`otelnats` library** (no app code) | NATS producer/consumer spans stop; the round trip **still runs** |

There is **no** application-level flag gating the NATS hop. The backend must
**not** call `otelnats.WithTracingEnabled(...)` — that pins the connection
static and the relay can never change it. Global kill switch (env only):
`OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` must be on for any library
evaluation to run.

---

## Repo layout

- `frontend/` — browser app that starts a trace and calls the backend.
- `backend/` — Go HTTP service; continues the trace, installs OpenFeature for
  library flags, publishes/consumes on NATS.
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

---

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

---

## Running the demo

Requires `docker`, `kind`, `helm`, and `kubectl` locally.

```sh
make deploy      # use current kubectl context (or auto-pick a kind cluster),
                  # build+load images, install/upgrade charts, apply services
make port-forward  # use current kubectl context; wait Ready, open local ports
# alias: make pf
```

Cluster name is **not hard-coded**. Day-to-day targets use the current kubectl
context (Docker Desktop, an existing kind context, etc.). To pin a kind cluster:

```sh
make deploy CLUSTER_NAME=my-kind
make port-forward CLUSTER_NAME=my-kind
make teardown CLUSTER_NAME=my-kind
```

When `CLUSTER_NAME` is unset: use the current `kind-*` context name, else the
sole kind cluster if exactly one exists, else create `demo-trace`. If kubectl
already points at a reachable non-kind cluster (e.g. Docker Desktop), kind
create/load is skipped and helm/apply run against that cluster.

`make deploy` is safe to re-run: existing clusters are **reused** (not
recreated); Helm upgrades in place and manifests are re-applied. `make teardown`
only deletes a kind cluster — never Docker Desktop or other contexts.

### Accessing the demo

After `make deploy`, start port-forwards with a single command (no Ingress —
see design.md for why):

```sh
make port-forward
```

This waits for core deployments, then forwards:

| Local URL | Service | Notes |
|---|---|---|
| http://localhost:8081 | frontend | open this and click **Start Trace** |
| http://localhost:8080 | backend | browser calls this directly |
| http://localhost:3000 | grafana | `admin` / `demo-grafana-admin` |
| http://localhost:4318 | rotel | browser OTLP (frontend spans) |

Leave that terminal running; Ctrl-C stops all forwards. Override ports if needed:

```sh
make port-forward PF_FRONTEND_PORT=9081 PF_GRAFANA_PORT=3001
```

---

## How to verify instrumentation is working

Use this checklist after `make deploy` and the port-forwards above. Treat it
as a manual acceptance test for the libraries this demo consumes.

### 1. Happy path — one click

1. Open `http://localhost:8081`.
2. Click **Start Trace**.
3. Expect UI fields: `traceId` / `spanId` present (hex IDs).
4. Open Grafana (`http://localhost:3000`, `admin` / `demo-grafana-admin`).
5. Open the provisioned **trace** dashboard (or Explore → ClickHouse traces).
6. Paste the `traceId` from the UI.

**Pass criteria (synchronous path, same trace ID):**

| What you should see | Why it proves |
|---|---|
| Frontend CLIENT / fetch span | Browser SDK + fetch instrumentation |
| Backend `POST /api/demo-trace` SERVER span, **same trace ID** | W3C extract/inject across HTTP |
| NATS **publish** / PRODUCER-style span under that trace | `otelnats` publisher instrumentation |

**Pass criteria (async NATS path, linked traces):**

| What you should see | Why it proves |
|---|---|
| `process demo.trace.request` (and reply path) under **other** trace IDs | Consumer uses span links, not parent-child |
| Span links from consumer → producer context | Header propagation on NATS messages worked |
| Dashboard panel “Span-linked async spans” shows those consumers | End-to-end story is visible without forcing one waterfall |

If the UI shows success but Grafana has **no** backend SERVER span sharing the
frontend `traceId`, propagation or export is broken (check port-forwards,
CORS `traceparent`, and rotel → ClickHouse).

### 2. Library flag — turn NATS instrumentation off live

This is the **only** flag verification this demo requires.

```sh
kubectl edit configmap demo-feature-flags -n demo
# set otel-nats-tracing defaultRule.variation to: disabled
```

Wait ~1s (relay re-reads the ConfigMap; nothing restarts). Click **Start Trace**
again.

| Expect | Meaning |
|---|---|
| UI still returns `traceId` / `spanId` (success) | NATS business path still ran |
| **No** new NATS producer/consumer spans in Grafana | `otelnats` honored `otel-nats-tracing` without app changes |

Flip back to `enabled` and confirm spans return on the next click.

### 3. Metrics smoke check (optional)

```sh
kubectl port-forward -n demo svc/victoria-metrics 8428:8428
curl -s 'http://localhost:8428/api/v1/query?query=up'
```

Scraped targets should report `1`. Grafana’s **Ecosystem Metrics** dashboard
should show rotel ingest and dependency health.

### 4. What “broken library” usually looks like

| Symptom | Likely cause |
|---|---|
| Success response but no NATS spans, with `otel-nats-tracing` enabled | `otelnats` not wrapping publish/subscribe, or global kill switch off (`OTEL_INSTRUMENTATION_GO_TRACING_ENABLED`) |
| NATS works but flipping `otel-nats-tracing` does nothing | Connection pinned with `WithTracingEnabled`, or OpenFeature provider not installed |
| Frontend and backend different trace IDs | Missing `traceparent` (CORS / fetch instrumentation / wrong backend URL) |
| Spans never appear in Grafana | OTLP export path (rotel, port-forward `4318`, ClickHouse) |

---

## Feature flags (reference)

The GO Feature Flag relay proxy reads its flag definitions straight from the
`demo-feature-flags` ConfigMap through the Kubernetes API. Edit it live — the
relay proxy re-reads within a second and **nothing restarts**:

```sh
kubectl edit configmap demo-feature-flags -n demo
```

If the relay proxy is unreachable, library evaluations fall back to
`OTEL_NATS_TRACING_ENABLED`, so the demo still works.

---

## Metrics

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

---

## Performance testing

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

---

## Tearing down

```sh
make teardown     # deletes the kind cluster and everything in it
```
