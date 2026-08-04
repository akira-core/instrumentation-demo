# instrumentation-demo

End-to-end demo of **W3C trace propagation** and the sibling instrumentation
libraries (`instrumentation-go` / `instrumentation-js`) running in a local
`kind` cluster.

A browser frontend starts a trace, calls a Go backend over HTTP, the backend
publishes and consumes on NATS via `otelnats`, and the whole path is exported
through `rotel` → ClickHouse and inspected in Grafana — including metrics via
VictoriaMetrics. A GOFF relay proxy serves **library** feature flags
(e.g. `otel-nats-tracing`) so you can prove an operator can **revoke**
instrumentation on a running process, with no application code and no restart.

**Languages:** [繁體中文（README.zh-TW.md）](README.zh-TW.md)

---

## What this demo is for

Use this stack to **check that our instrumentation libraries work in a
realistic path**, not only in unit tests:

| Library / package | Role in this demo |
|---|---|
| **`otelnats`** (`instrumentation-go`) | NATS publish/subscribe spans, W3C headers on messages, async **span links**, runtime kill switch `otel-nats-tracing` |
| **OpenFeature + GOFF provider** (**zero application code**) | `otelnats` installs its own provider from `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` and resolves `otel-nats-tracing` per operation |
| **Browser OTel** (`@opentelemetry/sdk-trace-web` + fetch instrumentation) | CLIENT span + `traceparent` injection on `POST /api/demo-trace` |
| **`@akira-core/otel-nats`** (`instrumentation-js`, submodule) | Available in-tree for JS NATS work; this UI demo focuses on the HTTP→Go→NATS path |

If spans appear correctly in Grafana after a click, and revoking a flag stops
library instrumentation live while the business path keeps running, the
libraries are wired the way production apps should wire them.

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
│       └──────────────────► Backend (Go)               Relay proxy (GOFF)    │
│                                  │  ▲                 ▲                     │
│                                  │  └─otelnats polls──┘ ConfigMap           │
│                                  │    (no app code)   │ demo-feature-flags  │
│                                  ▼                    │ (otel-nats-tracing) │
│                               NATS                    │                     │
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
| **backend** | `backend/` | SERVER span, NATS request/reply via `otelnats` (no feature-flag code of its own) |
| **NATS** | vendored chart | Message bus for the demo round trip |
| **relay-proxy** | GOFF chart | Serves the library kill switch `otel-nats-tracing` from a live ConfigMap |
| **rotel** | ClickHouse chart (subchart) | OTLP collector → ClickHouse (+ pipeline metrics → VM) |
| **ClickHouse** | vendored chart (Altinity operator) | Trace storage (CHI + Keeper) |
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

There is **no** application-level flag gating the NATS hop, and **no backend
code touches OpenFeature at all**. Setting
`OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` is the entire wiring: `otelnats`
builds its own GO Feature Flag provider on the first instrumented operation,
binds it to its private OpenFeature domain (`otel-instrumentation-go`), and
hardcodes the two settings that are easy to get wrong by hand — in-process
evaluation and a disabled data collector. It never touches the *default*
provider, so an application's own flags are unaffected.

Tracing is the conjunction of **three tiers**, and the relay is the only one
that can change without a redeploy:

```
tracing = OTEL_INSTRUMENTATION_GO_TRACING_ENABLED   (env, whole process)
       && OTEL_NATS_TRACING_ENABLED                 (env, this module)
       && relay flag otel-nats-tracing              (resolved per operation)
```

**The relay can only revoke.** It cannot enable what the environment left off —
with either variable falsy, `otelnats` allocates only its passthrough
implementation and never evaluates the flag at all. Both are set truthy in
`deploy/base/backend.yaml`, which is what puts this demo under relay control.
Every failure path — relay unreachable, provider not yet fetched, key missing,
wrong type — resolves to "no opinion", so the environment alone decides and the
demo keeps working with the relay down.

The backend must **not** call `otelnats.WithTracingEnabled(...)`: since 0.8.0
that option and `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` are two spellings of
one switch and are mutually exclusive, so passing both is a construction error
(`ErrTracingConfigConflict`). It no longer pins a connection — a connection
carrying it still follows the relay.

---

## Repo layout

- `frontend/` — browser app that starts a trace and calls the backend.
- `backend/` — Go HTTP service; continues the trace and publishes/consumes on
  NATS. Contains no feature-flag code: `otelnats` wires itself to the relay
  from the environment.
- `deploy/` — `kind` cluster config, Kustomize base for the in-house
  services, and Helm values for the vendored charts.
- `charts/` — vendored third-party Helm charts (NATS, ClickHouse via the
  Altinity operator umbrella, Grafana, VictoriaMetrics, and the GO Feature Flag
  relay proxy). Each subdirectory has a `SOURCE.txt` recording the exact chart
  version pulled.
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

### 2. Library flag — revoke NATS instrumentation live

This is the **only** flag verification this demo requires.

```sh
kubectl edit configmap demo-feature-flags -n demo
# set otel-nats-tracing defaultRule.variation to: disabled
```

Wait **~3s** — two hops, neither of which restarts anything: the relay re-reads
the ConfigMap (`pollingInterval: 1000`), then the backend's provider polls the
relay (`OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL: 2s`). Then click **Start
Trace** again.

| Expect | Meaning |
|---|---|
| UI still returns `traceId` / `spanId` (success) | NATS business path still ran |
| **No** new NATS producer/consumer spans in Grafana | `otelnats` honored the revocation without app changes |

Restore `enabled` and confirm spans return on the next click. Note what that
proves and what it does not: restoring the flag **lifts a revocation**, it does
not enable anything. Spans come back only because both environment tiers are
on. Set `OTEL_NATS_TRACING_ENABLED=0` on the Deployment and no flag value will
bring them back — that is the point of a revoke-only relay, and
`TestRelayCannotEnableNatsInstrumentation` pins it.

In production the default poll interval is **60s**, not 2s. Plan an incident
response around the poll interval, not around "immediately".

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
| Success response but no NATS spans, with `otel-nats-tracing` enabled | `otelnats` not wrapping publish/subscribe, or an env tier is off — the relay cannot enable what `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` / `OTEL_NATS_TRACING_ENABLED` left off |
| Backend logs `unrecognised boolean value; treated as disabled` | An env switch is set to something outside `1`/`true`/`yes`/`on`. An empty string counts as off |
| NATS never connects, logs a config error on every retry | `otelnats.WithTracingEnabled(...)` reintroduced in `natsflow` while the env var is also set — they are mutually exclusive (`ErrTracingConfigConflict`) |
| Revoking `otel-nats-tracing` does nothing | `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` unset (no provider installed), or fewer than ~3s elapsed since the edit |
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

The relevant environment variables, all read by `otelnats` itself:

| Variable | Set to | Purpose |
|---|---|---|
| `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` | `1` | Process-wide kill switch. No relay counterpart |
| `OTEL_NATS_TRACING_ENABLED` | `1` | This module's tier. Off ⇒ zero-cost passthrough, flag never evaluated |
| `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` | `http://relay-proxy:1031` | Unset ⇒ no provider is installed and no revocation is possible |
| `OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL` | `2s` | Go duration string. Library default `60s` |
| `OTEL_SERVICE_NAME` | `demo-backend` | Doubles as the `service.name` targeting attribute on every evaluation |

Because `OTEL_SERVICE_NAME` is supplied as a targeting attribute, a relay rule
can revoke one service instead of every process resolving the flag:

```yaml
otel-nats-tracing:
  variations: { enabled: true, disabled: false }
  targeting:
    - query: service.name eq "demo-backend"
      variation: disabled
  defaultRule:
    variation: enabled
```

If the relay proxy is unreachable, or has not been reached yet, evaluations
resolve to "no opinion" — which means *do not interfere* — so the environment
alone decides and the demo still works. The cost of that fail-safe: a process
that starts while the relay is down comes up instrumented and cannot learn about
an already-active revocation until its first successful poll.

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
