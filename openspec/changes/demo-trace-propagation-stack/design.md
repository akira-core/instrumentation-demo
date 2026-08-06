## Context

Greenfield repo (README stub only, no code). Two sibling repos provide the instrumentation building blocks but are **not published** to any registry:
- `instrumentation-js`: `@akira-core/otel-nats` v0.1.0 (pnpm workspace package, unpublished).
- `instrumentation-go`: `otelnats`/`oteljetstream` (tagged up to v0.7.0, installable via `go get` off git tags since it's a public-reachable module path) plus `otel-sampler`, `otel-testkit` (untagged, test-only).

Neither sibling repo has HTTP-client instrumentation or a feature-flag relay proxy — those are new for this demo. `instrumentation-go` ships OpenFeature + GO Feature Flag integration with a four-step precedence ladder (`relay > env > option > default`). This demo runs the real GOFF relay proxy and feeds it flag definitions from a Kubernetes ConfigMap **mounted as a volume** (file retriever), so operators edit cluster config without giving the relay API credentials.

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

### 5. Feature-flag relay proxy: GOFF with ConfigMap volume mount + ladder posture
The demo runs the upstream **GO Feature Flag relay proxy** (official Helm chart `go-feature-flag/relay-proxy`, vendored into `charts/relay-proxy`). Flag definitions live in the in-cluster ConfigMap `demo-feature-flags` (`flags.yaml` key). That ConfigMap is **volume-mounted** into the relay pod; GOFF uses the file retriever:

```yaml
retriever:
  kind: file
  path: /flags/flags.yaml
```

The vendored chart is patched with optional `extraVolumes` / `extraVolumeMounts` (values-driven). The volume references `demo-feature-flags` with `optional: true` so `helm-install` before `k8s-apply` still works; `startWithRetrieverError: true` keeps the process up until the file appears. Updates to the ConfigMap propagate through the kubelet mount refresh + GOFF `pollingInterval` (kept short for the demo) — no relay restart, and **no ConfigMap API RBAC**.

- **Superseded**: the `extraVolumes` / `extraVolumeMounts` patch and the `demo-feature-flags` ConfigMap are gone. Flag definitions now ship inside the chart as one file per concern under `charts/relay-proxy/config/`; the chart's `flags.*` values render them into the `relay-proxy-flags` ConfigMap, mount it, and generate one `file` retriever per file. Two consequences follow. The relay proxy has its flags from its first start, so `optional: true` and `startWithRetrieverError: true` are no longer needed — the latter is now `false`, and an unreadable retriever stops the pod instead of leaving it Ready while every evaluation returns the caller's default. And because the mount and the retrievers are templated from the same directory, they cannot drift apart: the earlier arrangement let a values file keep setting `extraVolumes` after the chart stopped rendering it, with nothing failing. The kubelet-refresh + `pollingInterval` propagation path and the absence of ConfigMap API RBAC are unchanged.

`otelnats` resolves `otel-nats-tracing` through OpenFeature under the library ladder `relay > env > option > default`. The relay is authoritative in **both** directions. Demo posture (**option C**):

| Knob | Deployment value | Role |
|---|---|---|
| `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` | `1` | Master veto stays open |
| `OTEL_NATS_TRACING_ENABLED` | `false` | Module env explicitly off |
| ConfigMap `otel-nats-tracing` | default `enabled` | Relay **enables** tracing |
| `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` | `http://relay-proxy:1031` | Zero-code provider auto-install |
| `OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL` | `2s` | Demo-friendly revocation latency |

The backend does **not** register an OpenFeature provider; `otelnats` auto-installs a named provider from the endpoint env var (in-process evaluation, `DataCollectorDisabled` hardcoded). Application handlers never evaluate flags. Do not call `otelnats.WithTracingEnabled(...)` on the demo connection — leave the option rung silent so relay and env decide.

ConfigMap content is **library flags only** (at minimum `otel-nats-tracing`). Do not reintroduce application-level demo flags.

- **Superseded**: GOFF `kind: configmap` Kubernetes API retriever + Role/RoleBinding for ConfigMap reads — replaced by the volume mount + file retriever so the relay needs no API access for flags.
- **Superseded**: revoke-only kill-switch demo posture (`OTEL_NATS_TRACING_ENABLED=1`, relay only subtracts) — replaced by ladder option C above after `instrumentation-go` made the relay authoritative both ways.
- **Superseded**: backend-installed GOFF provider (`internal/featureflags`, `SetProviderAndWait`) — replaced by the library zero-code path.
- **Superseded approach**: hand-written Go relay-proxy (`SharedInformer` + bespoke `/evaluate/:flagKey`) — removed earlier; could not drive `otelnats` through OpenFeature.
- **Superseded**: dual-flag ConfigMap (`otel-nats-tracing` + `demo-nats-flow`) and handler-side OpenFeature evaluation.
- **Alternative considered**: keep the API ConfigMap retriever — rejected; user required a ConfigMap **mount**, and the mount path removes RBAC for the same live-edit story (directory mount, not `subPath`, so kubelet refreshes the file).
- **Alternative considered**: GrowthBook's relay proxy — rejected; fronts GrowthBook SaaS/API, unrelated to `instrumentation-go`'s OpenFeature integration.

### 6. kind cluster topology: vendored third-party Helm charts + plain manifests for in-house services
Single-node `kind` cluster (`deploy/kind-cluster.yaml`), one namespace (`demo`). Third-party infrastructure with mature upstream Helm charts — NATS, ClickHouse, Grafana, and the GOFF relay proxy — is vendored into a single top-level `charts/` directory (`helm pull <chart> --untar -d charts/`, one subdirectory per chart, each with a `SOURCE.txt` recording the source repo and chart version pulled), deployed via `helm install <name> charts/<component> -f deploy/values/<component>.yaml -n demo`. Re-implementing ClickHouse's StatefulSet/PVC handling or Grafana's plugin/provisioning wiring by hand would just be a worse copy of what those charts already do well. rotel (no known official chart) and this repo's own services (frontend, backend, the flags ConfigMap) stay plain Kubernetes manifests under a Kustomize base (`deploy/base/`, `kubectl apply -k deploy/base`) since they're small, project-specific, and change often during development. The full deploy is two tool invocations wrapped by one `make deploy` target. Images are built locally and loaded via `kind load docker-image` (no registry needed). Grafana and the backend's HTTP API are reachable via `kubectl port-forward` (documented in tasks/README) rather than an Ingress controller, to minimize moving parts.
- **Alternative considered**: pure Kustomize with hand-written manifests for every component (the original decision) — superseded per explicit request for a unified third-party-chart directory; hand-rolling ClickHouse/Grafana would also have been strictly more implementation work than reusing the maintained charts.
- **Alternative considered**: one umbrella Helm chart wrapping everything, including the in-house services — rejected; adds Helm templating overhead to services that change on every demo-development iteration, for no reuse benefit since they're not shared outside this repo.

## Risks / Trade-offs

- [Submodules track a branch, not a pinned commit — a fresh clone without `--recurse-submodules`, or a stale checkout that never ran `git submodule update --remote`, silently builds against old or missing sibling code] → Document `git clone --recurse-submodules` and `git submodule update --init --remote` in README as required setup; a `make bootstrap` target runs the init/update automatically so it isn't a manual step to forget.
- [Vendored charts under `charts/` can drift from upstream as NATS/ClickHouse/Grafana cut new chart releases] → `SOURCE.txt` per chart records the exact chart version pulled; upgrading is an explicit `helm pull` re-run and diff review, never automatic.
- [rotel is younger/less battle-tested than otelcol-contrib; ClickHouse exporter behavior or schema could change between rotel versions] → Pin rotel's image tag explicitly in the manifest; document the pinned version in design so upgrades are a conscious choice.
- [The NATS hop's spans land under different trace IDs than the HTTP hop, which reads as "broken propagation" to anyone expecting one waterfall] → This is `otelnats`'s documented, intentional span-link design (Decision 4), not a defect. Mitigated by making it explicit everywhere it would surprise someone: in the specs, in the Grafana dashboard (which must surface span links, not just a single-trace waterfall), and in the frontend's result display.
- [ConfigMap volume refresh is not instant — kubelet sync period plus GOFF poll] → Accepted for a demo; keep `pollingInterval` short and document that flips take a few seconds, not milliseconds. Prefer a directory mount (not `subPath`) so updates are visible.
- [helm-install before k8s-apply means the flags ConfigMap may be missing at relay start] → Mitigated with `optional: true` on the volume and `startWithRetrieverError: true`; once `k8s-apply` creates the ConfigMap, the mount populates and GOFF picks up the file.
- [With `OTEL_NATS_TRACING_ENABLED=false`, an unreachable relay yields no NATS spans] → Intended under option C: the env rung is off, so "relay has no opinion" stays silent. Happy-path verification requires the relay and an enabled ConfigMap flag.
- [Without a per-request app flag evaluation, the synchronous trace may not show a relay CLIENT span] → Accepted. The proof that library flags work is NATS spans appearing/disappearing when `otel-nats-tracing` is flipped, not an application OpenFeature span in the waterfall.
- [Using `otelnats.WithTracingEnabled(...)` on the demo connection muddies which ladder rung decided] → Construct the demo connection without that option so relay and env alone explain the outcome; the option is legal in the library but out of scope for this demo's verification story.
- [Single-node kind cluster + `kubectl port-forward` doesn't demonstrate real ingress/networking patterns] → Acceptable for a trace-propagation demo; explicitly out of scope per Goals.
- [Backend simulating both producer and consumer in one binary understates real NATS deployment topology] → Documented as a deliberate simplification (Decision 4 alternative); acceptable since the NATS boundary is what's being proven, not process topology.

## Migration Plan

Net-new repo content, nothing to migrate. Rollback is `git revert` of this change's commit(s) plus `kind delete cluster` — no persistent external state outside the ephemeral `kind` cluster.

## Open Questions

- Exact rotel image tag/version to pin — resolve during tasks implementation by checking rotel's latest stable release at build time; doesn't change the spec or approach.
- Whether Grafana dashboard JSON is hand-written or generated via `grafana-clickhouse-datasource`'s dashboard import — an implementation detail for tasks.md, not a spec-level concern.
