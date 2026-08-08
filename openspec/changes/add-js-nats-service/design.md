## Context

The demo currently runs two in-house images: `demo-backend` (Go, HTTP + NATS + `otelnats` + `otel-flags`) and `demo-frontend` (browser OTel over nginx). `third_party/instrumentation-js` is checked out as a submodule and built by nothing.

`instrumentation-js`'s `add-dynamic-feature-flags` gives `@akira-core/otel-nats` the same four-rung ladder the Go module has, resolving the same `otel-nats-tracing` key through a GO Feature Flag relay. The two runtimes deliberately share that **module** key and keep separate **master** keys (`otel-instrumentation-go-tracing` / `otel-instrumentation-js-tracing`) and separate OpenFeature domains (`otel-instrumentation-go` / `otel-instrumentation-js`) — a domain names a provider binding inside one process, and a Go process and a Node process never share one.

The existing evidence pipeline is the constraint that shapes most of this design. `docs/scripts/capture-live-evidence.sh` runs three campaigns (enabled → disabled → enabled), each capturing the ConfigMap, the relay's evaluation response, the API response, and a ClickHouse span query, and `docs/scripts/render-flag-matrix-html.py` renders them. Those artifacts are the demo's product. Anything that changes what a Go-only campaign measures would invalidate the published report.

## Goals / Non-Goals

**Goals:**

- Run a JS service in the cluster that produces and consumes NATS messages through `@akira-core/otel-nats`, exporting to the same collector.
- Prove one ConfigMap flip changes both runtimes' instrumentation, in both directions, with neither restarted.
- Show trace linking across a language boundary: a JS consumer root linked to a Go producer span.
- Leave the Go backend's code, manifest, and evidence semantics untouched.

**Non-Goals:**

- Routing browser traffic through the JS service, or giving it an HTTP surface beyond a liveness probe. It is a messaging participant; adding HTTP would duplicate what the Go backend already demonstrates.
- JetStream. The Go backend's demo path is core NATS; matching it keeps the comparison honest. `@akira-core/otel-nats/jetstream` gets its coverage in the package's own integration suite.
- Replacing or load-balancing the Go backend's subscriber. See the topology decision below.
- Porting the load generator or the metrics pipeline to JS.

## Decisions

### Topology: an additive fan-out subscriber, not a link in the existing chain

```
POST /api/demo-trace
      │
      ▼
Go backend ──publish──► demo.trace.request ──┬──► Go subscriber ──publish──► demo.trace.reply ──► Go subscriber
                                             │
                                             └──► JS subscriber ──publish──► demo.trace.js ──► JS subscriber
```

The JS service subscribes to `demo.trace.request` **without a queue group**. Core NATS delivers to every distinct subscription, and queue groups only load-balance within a group, so an ungrouped JS subscriber receives every message the Go subscriber also receives. Nothing is stolen and the Go round trip is unaffected — the reply still arrives, `RunRoundTrip` still returns, and `live-summary.json`'s existing span counts for the Go path stay as they are.

Alternatives considered:

- **Insert the JS service into the chain** (Go publishes → JS consumes → JS publishes the reply → Go's reply subscriber resolves the correlation). This is the more impressive topology, and it is rejected: it makes the demo's HTTP response depend on a second service being healthy, so a JS-side failure or a JS-side flag flip that broke publishing would look like a broken demo rather than a flag experiment. It also changes what the Go backend's existing evidence measures.
- **Join the Go subscriber's queue group.** Rejected outright: it would take roughly half the messages away from the Go subscriber, halving its span counts and invalidating every published Go campaign.

The second JS hop (`demo.trace.js`, published and consumed by the same service) exists so the JS service exercises **both** sides of the library — a PRODUCER span with header injection and a CONSUMER root with extraction and a span link — rather than only the consumer side. It costs one subject and no coordination.

### Flag posture mirrors the backend exactly

```
OTEL_INSTRUMENTATION_JS_TRACING_ENABLED=1        master veto open
OTEL_NATS_TRACING_ENABLED=false                  module env off
OTEL_INSTRUMENTATION_JS_FLAGS_ENDPOINT=http://relay-proxy:1031
OTEL_INSTRUMENTATION_JS_FLAGS_POLL_INTERVAL=2s
OTEL_SERVICE_NAME=demo-js-service
```

Same "option C" as `deploy/base/backend.yaml`: the module's own environment says **off**, so spans appearing at all is only explicable by the relay having enabled them. The application passes no `tracingEnabled` option, for the same reason the Go backend passes no `WithTracingEnabled` — a demo-only static option would muddy exactly what the verification is trying to isolate.

`OTEL_INSTRUMENTATION_JS_FLAGS_REFRESH_INTERVAL` is left unset so it defaults to the poll interval, keeping the JS-side latency bound at `relay pollingInterval (1s) + provider poll (2s) + snapshot refresh (≤2s)`. That is genuinely looser than the Go path's `1s + 2s`, and the README states it rather than rounding it away — the JS snapshot is an extra hop and pretending otherwise would make a flake look like a bug.

### One image, `node_modules` included

The service is built from the repo root as its Docker context so the `third_party/instrumentation-js` workspace is reachable, mirroring why `backend/Dockerfile` builds from the root to reach `third_party/instrumentation-go`.

The runtime image ships `node_modules` rather than a bundle. The GO Feature Flag provider's in-process evaluator loads a WASM binary from its own package directory at runtime; a bundler that inlines JS but not assets produces a provider that cannot initialise, which surfaces as a permanently dead relay connection rather than a build error. Shipping `node_modules` sidesteps the whole class of problem for an image nobody publishes. A multi-stage build with `pnpm deploy --prod` keeps it to production dependencies.

### Liveness and readiness never depend on NATS or the relay

`/healthz` on a small HTTP listener returns OK once the process is up, exactly like the Go backend's — which explicitly never blocks on NATS. The NATS connection is established with retry and backoff in the background.

This matters more here than it looks: if readiness waited on the relay, then flipping `otel-nats-tracing` to `disabled` could restart the pod, and a restart would destroy the very thing the demo proves (that the change reaches a **running** process). Readiness must be blind to the flag.

### Evidence capture asserts on both services, per campaign

`capture-live-evidence.sh` gains a parallel ClickHouse query per campaign filtered to `ServiceName = 'demo-js-service'`, written to `live-step-c<N>-clickhouse-js.txt` alongside the existing file, and `live-summary.json` gains a per-campaign JS span count next to the existing counts. Existing keys keep their names and their meaning; the JS data is additive.

The assertion each campaign makes is the pointed one: in the enabled campaigns both services' NATS span counts are non-zero; in the disabled campaign **both** are zero while the HTTP request still succeeds and the JS service still logs a consumed message. "Business path keeps running while instrumentation stops" is the claim, and it has to be visible on both sides.

The renderer gains a column rather than a second report. One table showing both runtimes under one flag flip is the whole point; two reports would let a reader miss that it was one flip.

### The submodule pin is part of this change

`third_party/instrumentation-js` currently tracks `feat/otel-nats`. This change bumps the gitlink to a commit carrying `add-dynamic-feature-flags`, the same way `third_party/instrumentation-go` tracks `feat/inprogress-openfeature`. `.gitmodules`' `branch` entry is updated with it, and the Dockerfile pins nothing further — the gitlink is the pin.

## Risks / Trade-offs

- **[Risk] The JS snapshot refresh adds latency the Go path does not have,** so a campaign that samples too soon after a flip could see one runtime changed and the other not, and read as a bug. → **Mitigation**: the capture script's post-flip wait is derived from the **larger** of the two bounds, and the README documents both bounds separately.
- **[Risk] An ungrouped subscriber changes NATS server-side fan-out,** so the request subject now has two deliveries per message. → **Assessed as immaterial**: it is one extra delivery of a small message on a single-node NATS in a kind cluster, and the Go subscriber's own delivery is unchanged. Called out because the metrics pipeline scrapes NATS, so the message-count series will step up when this lands.
- **[Risk] `demo.trace.js` is consumed by the same process that publishes it,** which is a slightly artificial shape. → **Accepted**: it exercises both library sides without a second image, and the README says plainly that it is a self-loop for demonstration rather than a realistic service boundary.
- **[Risk] Shipping `node_modules` makes the image larger than a bundled one.** → **Accepted**: it is a local kind image that is never pushed, and the WASM failure mode it avoids is silent.
- **[Trade-off] The JS service is not on the HTTP request path,** so a reader cannot see its effect by watching the API response — only by querying spans. Accepted: putting it on the request path would make a flag experiment able to break the demo.
- **[Risk] The published Go-only evidence report and the new two-runtime report can drift.** → **Mitigation**: the renderer emits one report covering both, and the old Go-only artifacts are regenerated rather than kept alongside.

## Migration Plan

Additive. `make deploy` on an existing cluster rolls out the new Deployment and leaves everything else untouched; `make teardown` is unchanged. A cluster running the previous revision keeps working — the new subject and subscription simply do not exist there.

Rollback is removing `js-service.yaml` from the Kustomize base and re-applying; nothing else depends on it.

Evidence regeneration happens once, after the service is live, and replaces `docs/evidence/` and both rendered HTML reports in one commit so no reader ever sees a half-updated set.

## Open Questions

_(none outstanding)_
