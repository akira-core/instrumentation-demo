## Context

Greenfield repo (README stub only, no code). Two sibling repos provide the instrumentation building blocks but are **not published** to any registry:
- `instrumentation-js`: `@akira-core/otel-nats` v0.1.0 (pnpm workspace package, unpublished).
- `instrumentation-go`: `otelnats`/`oteljetstream` (tagged up to v0.7.0, installable via `go get` off git tags since it's a public-reachable module path) plus `otel-sampler`, `otel-testkit` (untagged, test-only).

Neither sibling repo has HTTP-client instrumentation or a feature-flag relay proxy — those are new for this demo. `instrumentation-go` already has a proven pattern for dynamic-flag gating via OpenFeature + a relay proxy (the archived `openfeature-dynamic-flags` change, backed by GO Feature Flag), which this demo's relay proxy imitates architecturally but replaces with a Kubernetes-native config source instead of GOFF, per the user's requirement that "relay proxy read the k8s config."

See proposal.md - Why / What Changes for motivation and scope. This document covers only cross-cutting technical decisions.

## Goals / Non-Goals

**Goals:**
- One `kind` command + one deploy command bring up all 8 components (frontend, backend, NATS, relay proxy, rotel, ClickHouse, Grafana, plus the kind cluster itself) with a single trace visible end-to-end in Grafana.
- Demonstrate W3C trace context crossing three boundaries: browser→HTTP, HTTP→NATS (via `otelnats`), and service→relay-proxy (HTTP).
- Keep the demo reproducible via git submodules pointing at the sibling repos (not vendored copies), so a fresh clone with `--recurse-submodules` gets exactly the code the demo was built against, while remaining trivial to advance to a newer commit on the tracked branch on request.

**Non-Goals:**
- Production-grade HA, autoscaling, or multi-tenant relay proxy.
- Full OpenFeature spec compliance for the relay proxy (a minimal evaluation API is enough to prove the pattern).
- Persisting metrics/logs — traces only, since that's what's being demonstrated.
- Publishing `@akira-core/otel-nats` to npm or cutting a new `otelnats` release — this change consumes what already exists.

## Decisions

### 1. Cross-repo dependency strategy: git submodules, tracking a branch (not pinned)
`instrumentation-js` and `instrumentation-go` are added as git submodules under `third_party/` (`third_party/instrumentation-js`, `third_party/instrumentation-go`). Each `.gitmodules` entry sets an explicit `branch =` so `git submodule update --remote` advances to that branch's current tip — the checked-out commit is still an explicit gitlink in the parent repo, but nothing pins it forever; updating is a deliberate, user-driven action, not automatic on every clone/pull. The branch is the user's choice and neither submodule tracks `main`:

| Submodule | Tracked branch | Why not `main` |
|---|---|---|
| `instrumentation-js` | `feat/otel-nats` | `main` is a bare README stub; `@akira-core/otel-nats` only exists on this branch |
| `instrumentation-go` | `feat/inprogress-openfeature` | carries the OpenFeature + GO Feature Flag relay integration (`otel-nats-tracing` dynamic flag) this demo is built around — see Decision 5 |

Keeping `.gitmodules` in sync with the intended branch matters more than it looks: with a stale `branch = main` entry the working tree can sit on the right commit while `make bootstrap` silently yanks it back to a branch missing the code the demo depends on. Because the submodule content is part of this repo's own working tree once initialized, both local dev and container builds use the *same* reference: `go.work` includes `third_party/instrumentation-go/otel-nats` and `.../oteljetstream` directly, and `frontend/package.json` depends on `@akira-core/otel-nats` via a workspace/`link:` path into `third_party/instrumentation-js/packages/otel-nats`. Docker builds use the repo root as build context so `COPY third_party/...` reaches the submodule content directly — no rsync/vendor-sync step, no build-context gymnastics.
- **Alternatives considered**: the previously-planned vendor-sync/rsync copy step is dropped — it required an explicit, easy-to-forget sync command and risked silent staleness with no visible diff; a submodule's pinned commit is always visible via plain `git status`/`git diff`. `go.mod replace` against `instrumentation-go`'s tagged releases (it does publish real tags) was considered for the Go side only, but rejected to keep one uniform mechanism across both languages, per the explicit request to bring both repos in as local submodules.

### 2. rotel as the OTel collector
Use `rotel` (the lightweight Rust OpenTelemetry collector) rather than upstream `otelcol-contrib`, per the explicit requirement. Frontend and backend both export OTLP/HTTP directly to rotel's in-cluster Service; rotel batches and exports to ClickHouse via its native ClickHouse exporter.
- **Alternative considered**: `otelcol-contrib` with the `clickhouseexporter` community component — more mature/documented, but the user specifically asked for rotel, and rotel's smaller footprint fits a `kind` demo better.

### 3. ClickHouse schema: rotel's default trace table layout
Let rotel create/manage its default OTLP trace schema in ClickHouse (single `otel_traces` table, standard OTel columns: trace_id, span_id, parent_span_id, span_name, span_kind, service_name, duration, status, attributes maps). Grafana queries this table directly via the official `grafana-clickhouse-datasource` plugin. No custom schema/migration — avoids maintaining a parallel schema definition that could drift from what rotel actually writes.

### 4. Trace flow topology and NATS subject
Single logical flow per user click:
1. Frontend: `@opentelemetry/sdk-trace-web` + `FetchInstrumentation` starts a CLIENT span, injects `traceparent`/`tracestate` into the `POST /api/demo-trace` request to the backend.
2. Backend HTTP handler: extracts context, starts a SERVER span, then **always** proceeds to the NATS round trip on a successful demo path (no application-level feature flag gates business behavior).
3. Backend publishes to NATS subject `demo.trace.request` via `otelnats.PublishMsg` (PRODUCER span when library tracing is on, W3C context injected into message headers).
4. A backend-side subscriber (same binary, separate goroutine, simulating a downstream consumer service) receives on `demo.trace.request` via `otelnats.Subscribe` (CONSUMER span when library tracing is on), does trivial work, and publishes a reply on `demo.trace.reply`.
5. Original handler receives the reply, closes its SERVER span, and returns `{traceId, spanId}` JSON to the frontend for on-screen display and a "View in Grafana" deep link.

**The NATS hop is span-linked, not a single trace ID — this is deliberate and load-bearing for the demo.** `otelnats`'s consumer wrapper starts each `process <subject>` span with `tracer.Start(context.Background(), ...)` and attaches the producer's span context as an OTel **span link**, not as a parent. Verified empirically against a live cluster: one demo request produces the HTTP/publish spans under the incoming trace ID and the NATS `process` spans under separate root trace IDs, cross-referenced by links. The library documents this as intentional ("Async consumers use span links rather than parent-child relationships — preserves causality without implying synchronous nesting"), and it follows OTel's own semantic-convention guidance for asynchronous messaging, where a consumer's lifetime is unrelated to the producer's.

So the demo's honest claim is: **trace context propagates across every boundary** (browser→HTTP, publisher→NATS message headers→consumer), and the consumer proves it received that context — but the resulting spans form a *linked* graph across 2–3 trace IDs rather than one waterfall. An earlier revision of the specs demanded a single end-to-end trace ID; that was written before checking the library and has been corrected, since forcing it would have meant bypassing `otelnats`'s own span creation and hand-rolling parent-child linkage — reimplementing precisely what the library deliberately chose not to do, and no longer demonstrating the real library.
- **Alternative considered**: two separate backend binaries (producer service + consumer service) more realistically mirrors a real microservice split, but doubles the k8s manifests/images for no additional trace-propagation insight since the interesting boundary is NATS, not process separation; deferred as a possible follow-up, not required for this demo.
- **Superseded**: an application-level `demo-nats-flow` flag once gated the NATS hop and returned `flagEnabled` / `natsFlowExecuted` in the API so operators could prove app OpenFeature wiring. That diluted the demo's purpose (proving **library** flags like `otel-nats-tracing`) and forced a two-flag mental model. Removed: NATS always runs; the only ConfigMap flag operators flip for verification is library instrumentation.

### 5. Feature-flag relay proxy: the real GO Feature Flag (GOFF) relay proxy, ConfigMap-backed
The demo runs the upstream **GO Feature Flag relay proxy** (official Helm chart `go-feature-flag/relay-proxy`, vendored into `charts/relay-proxy`), configured with GOFF's native Kubernetes ConfigMap retriever:

```yaml
retrievers:
  - kind: configmap
    namespace: demo
    configmap: demo-feature-flags
    key: flags.yaml
```

This is the relay proxy `instrumentation-go` itself integrates with. `otelnats` resolves its own `otel-nats-tracing` flag through the process-global OpenFeature client at runtime, so flipping that flag on the relay turns NATS instrumentation on/off **without restarting the backend** — the headline capability this demo exists to showcase. The backend registers the OpenFeature GOFF provider at startup (`gofeatureflag.NewProvider(ProviderOptions{Endpoint: "http://relay-proxy:1031"})` + `openfeature.SetProviderAndWait`), exactly the wiring the sibling repo's README tells applications to copy; the instrumentation library never installs a provider itself. Application request handlers do **not** evaluate feature flags to gate demo behavior.

ConfigMap content is **library flags only** (at minimum `otel-nats-tracing`). Do not reintroduce application-level demo flags solely to exercise OpenFeature from handler code.

Provider options remain suitable for library-driven evaluation: the process installs a real GOFF provider so `otelnats`'s resolver can reach the relay. `otelnats`'s internal reads are capped by its 1-second snapshot cache, so the relay sees at most one library-driven request per second regardless of demo request rate. (An earlier revision used `EvaluationType: REMOTE` + `DisableCache: true` primarily to make a per-request **app** flag-evaluation CLIENT span visible; with app flags removed, that remote-per-request posture is optional for the demo path itself — keep whatever settings still allow live ConfigMap flips of `otel-nats-tracing` within about a second.)

RBAC: GOFF gets a ServiceAccount + `Role` scoped to `get`/`list`/`watch` on the single `demo-feature-flags` ConfigMap in the `demo` namespace — least-privilege, and the relay proxy is the only component talking to the Kubernetes API.
- **Superseded approach**: an earlier revision of this design specified a hand-written Go relay-proxy service (`relay-proxy/`, client-go `SharedInformer`, custom `GET /evaluate/:flagKey`). It was fully implemented and passing tests, then removed. Reason: it duplicated a mature upstream product badly, spoke a bespoke HTTP protocol nothing else understood, and — critically — could not drive `otelnats`'s dynamic flags, which only read through OpenFeature. Keeping it would have meant the backend pinning `otelnats.WithTracingEnabled(true)`, which the library documents as making a connection **fully static** ("no OpenFeature evaluation ever runs for it, no relay change reaches it") — i.e. the custom relay proxy actively defeated the feature it was supposed to demonstrate.
- **Superseded**: dual-flag ConfigMap (`otel-nats-tracing` + application `demo-nats-flow`) and handler-side OpenFeature evaluation — removed so verification focuses exclusively on library instrumentation flags.
- **Alternative considered**: mount the ConfigMap as a file and point GOFF's `kind: file` retriever at it — fewer moving parts and no RBAC, but a `subPath` mount does not update live, and it fails the "relay proxy reads the k8s config" requirement in spirit (that reads as Deployment-time config, not a Kubernetes-aware service). GOFF's `configmap` retriever talks to the API server directly, which is the intent.
- **Alternative considered**: GrowthBook's relay proxy (`growthbook-proxy`) — rejected because it fronts GrowthBook's SaaS/self-hosted API rather than Kubernetes config, would need a whole GrowthBook backend as an extra component, and has no relationship to the OpenFeature integration `instrumentation-go` actually ships.

### 6. kind cluster topology: vendored third-party Helm charts + plain manifests for in-house services
Single-node `kind` cluster (`deploy/kind-cluster.yaml`), one namespace (`demo`). Third-party infrastructure with mature upstream Helm charts — NATS, ClickHouse, Grafana, and the GOFF relay proxy — is vendored into a single top-level `charts/` directory (`helm pull <chart> --untar -d charts/`, one subdirectory per chart, each with a `SOURCE.txt` recording the source repo and chart version pulled), deployed via `helm install <name> charts/<component> -f deploy/values/<component>.yaml -n demo`. Re-implementing ClickHouse's StatefulSet/PVC handling or Grafana's plugin/provisioning wiring by hand would just be a worse copy of what those charts already do well. rotel (no known official chart) and this repo's own services (frontend, backend, the flags ConfigMap, the relay proxy's RBAC) stay plain Kubernetes manifests under a Kustomize base (`deploy/base/`, `kubectl apply -k deploy/base`) since they're small, project-specific, and change often during development. The full deploy is two tool invocations wrapped by one `make deploy` target. Images are built locally and loaded via `kind load docker-image` (no registry needed). Grafana and the backend's HTTP API are reachable via `kubectl port-forward` (documented in tasks/README) rather than an Ingress controller, to minimize moving parts.
- **Alternative considered**: pure Kustomize with hand-written manifests for every component (the original decision) — superseded per explicit request for a unified third-party-chart directory; hand-rolling ClickHouse/Grafana would also have been strictly more implementation work than reusing the maintained charts.
- **Alternative considered**: one umbrella Helm chart wrapping everything, including the in-house services — rejected; adds Helm templating overhead to services that change on every demo-development iteration, for no reuse benefit since they're not shared outside this repo.

## Risks / Trade-offs

- [Submodules track a branch, not a pinned commit — a fresh clone without `--recurse-submodules`, or a stale checkout that never ran `git submodule update --remote`, silently builds against old or missing sibling code] → Document `git clone --recurse-submodules` and `git submodule update --init --remote` in README as required setup; a `make bootstrap` target runs the init/update automatically so it isn't a manual step to forget.
- [Vendored charts under `charts/` can drift from upstream as NATS/ClickHouse/Grafana cut new chart releases] → `SOURCE.txt` per chart records the exact chart version pulled; upgrading is an explicit `helm pull` re-run and diff review, never automatic.
- [rotel is younger/less battle-tested than otelcol-contrib; ClickHouse exporter behavior or schema could change between rotel versions] → Pin rotel's image tag explicitly in the manifest; document the pinned version in design so upgrades are a conscious choice.
- [The NATS hop's spans land under different trace IDs than the HTTP hop, which reads as "broken propagation" to anyone expecting one waterfall] → This is `otelnats`'s documented, intentional span-link design (Decision 4), not a defect. Mitigated by making it explicit everywhere it would surprise someone: in the specs, in the Grafana dashboard (which must surface span links, not just a single-trace waterfall), and in the frontend's result display.
- [GOFF's `configmap` retriever only works in-cluster, so the relay proxy cannot be exercised outside `kind`] → Accepted; the whole demo is `kind`-scoped by design. Local development against the backend alone still works via the `OTEL_NATS_TRACING_ENABLED` env fallback, which is exactly the degradation path the library uses when the relay has no usable opinion.
- [Without a per-request app flag evaluation, the synchronous trace may not show a relay CLIENT span] → Accepted. The proof that library flags work is NATS spans appearing/disappearing when `otel-nats-tracing` is flipped, not an application OpenFeature span in the waterfall.
- [Pinning `otelnats.WithTracingEnabled(true)` anywhere would silently disable the demo's headline feature] → The library documents this option as making a connection fully static, with no OpenFeature evaluation ever running for it. The backend must therefore construct its connection *without* that option and let the flag resolve dynamically; `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=1` must also be set in the manifest, since it is an env-only kill switch with no relay counterpart that gates evaluation entirely.
- [Relay proxy needs Kubernetes API access] → ServiceAccount + `Role` scoped to `get`/`list`/`watch` on the single `demo-feature-flags` ConfigMap in the `demo` namespace (never a `ClusterRole`), and it is the only component in the demo that talks to the API server.
- [Single-node kind cluster + `kubectl port-forward` doesn't demonstrate real ingress/networking patterns] → Acceptable for a trace-propagation demo; explicitly out of scope per Goals.
- [Backend simulating both producer and consumer in one binary understates real NATS deployment topology] → Documented as a deliberate simplification (Decision 4 alternative); acceptable since the NATS boundary is what's being proven, not process topology.

## Migration Plan

Net-new repo content, nothing to migrate. Rollback is `git revert` of this change's commit(s) plus `kind delete cluster` — no persistent external state outside the ephemeral `kind` cluster.

## Open Questions

- Exact rotel image tag/version to pin — resolve during tasks implementation by checking rotel's latest stable release at build time; doesn't change the spec or approach.
- Whether Grafana dashboard JSON is hand-written or generated via `grafana-clickhouse-datasource`'s dashboard import — an implementation detail for tasks.md, not a spec-level concern.
