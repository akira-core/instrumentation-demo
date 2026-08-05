## 1. Repo scaffolding & cross-repo dependency wiring

- [x] 1.1 Create top-level `frontend/`, `backend/`, `deploy/`, `charts/` directory structure (a `relay-proxy/` Go module was created here in the first pass and later removed — see section 3)
- [x] 1.2 Add `instrumentation-js` and `instrumentation-go` as git submodules under `third_party/`, each tracking a branch (not a pinned commit): `instrumentation-js` → `feat/otel-nats` (its `main` is a stub with no `@akira-core/otel-nats`); `instrumentation-go` → `feat/inprogress-openfeature` (carries the OpenFeature + GOFF relay integration this demo is built around). Keep `.gitmodules` in sync with the intended branch, or `make bootstrap` silently reverts the checkout.
- [x] 1.3 Add `go.work` at repo root referencing `backend` and `third_party/instrumentation-go/otel-nats` (module root; `oteljetstream` is an importable subpackage of that same module, not a separate `go.work` member)
- [x] 1.4 ~~pnpm workspace/link wiring for `@akira-core/otel-nats` into `frontend/`~~ — dropped: per design.md Decision 4, the frontend only does browser HTTP tracing (`@opentelemetry/sdk-trace-web` + fetch instrumentation); NATS is backend-only, so the JS package has no consumer in this demo. `pnpm-workspace.yaml` covers `frontend` only.
- [x] 1.5 Add a `make bootstrap` target running `git submodule update --init --remote`, for fresh clones that skipped `--recurse-submodules`
- [x] 1.6 Document in root `README.md`: `git clone --recurse-submodules` for first checkout, and `git submodule update --remote third_party/<repo>` to advance a submodule to its tracked branch's latest commit (an explicit, user-initiated step, never automatic)

## 2. Backend service (Go)

- [x] 2.1 Scaffold Go module `backend/` with HTTP server, OTLP HTTP exporter setup, and W3C propagator configured
- [x] 2.2 Implement `POST /api/demo-trace` handler: extract incoming `traceparent`/`tracestate` (or start a root trace if absent), start a SERVER span
- [x] 2.3 ~~Implement relay-proxy client call (`GET /evaluate/demo-nats-flow`) as a CLIENT span~~ — **superseded by 3.4/3.5**, then **superseded by section 9**: no application-level flag evaluation on the request path
- [x] 2.4 ~~Wire `otelnats.ConnectWithOptions(..., WithTracingEnabled(true))`~~ — **superseded by 3.6**: that option makes the connection fully static and blocks every relay-driven flag change, defeating the demo's headline feature. Publishing to `demo.trace.request` with the active context is unchanged.
- [x] 2.5 Implement in-process subscriber on `demo.trace.request` (CONSUMER span via `otelnats.Subscribe`) that does trivial work and publishes a reply on `demo.trace.reply`
- [x] 2.6 Wire the original handler to await the reply, close its SERVER span, and return `{traceId, spanId}` JSON (app-flag fields were added later; **section 9 restores** the minimal contract)
- [x] 2.7 Add retry/backoff for the initial NATS connection so the backend doesn't crash-loop if NATS isn't ready yet
- [x] 2.8 Add a `/healthz` endpoint for k8s readiness/liveness probes
- [x] 2.9 Write a Dockerfile for the backend image, using repo root as build context so it can `COPY third_party/instrumentation-go/otel-nats`
- [x] 2.10 Write unit/integration tests for the handler covering: trace continuation, missing-traceparent root trace, and the NATS publish/consume/reply round trip

## 3. Feature-flag relay proxy (GO Feature Flag)

> Reworked per design.md Decision 5. The hand-written Go relay-proxy service built in the
> first pass (client-go informer + bespoke `/evaluate/{flagKey}`) is **removed**: it could not
> drive `otelnats`'s dynamic `otel-nats-tracing` flag, which resolves only through OpenFeature.
> Tasks 3.1–3.3 below record that removal; 3.4–3.9 build the replacement.

- [x] 3.1 ~~Scaffold Go module `relay-proxy/` (HTTP server + OTLP export)~~ — superseded; see note above
- [x] 3.2 ~~client-go `SharedInformer` watching `demo-feature-flags`, parsing JSON into an in-memory flag map~~ — superseded by GOFF's native `kind: configmap` retriever
- [x] 3.3 ~~Bespoke `GET /evaluate/:flagKey` + `/healthz` + Dockerfile + fake-clientset tests~~ — superseded by the upstream GOFF relay proxy image/chart
- [x] 3.4 Delete the `relay-proxy/` Go module and drop it from `go.work`; remove its image build from the Makefile and its `deploy/base/relay-proxy.yaml` manifest
- [x] 3.5 Vendor the official GOFF relay-proxy Helm chart into `charts/relay-proxy` (`helm repo add go-feature-flag https://charts.gofeatureflag.org/ && helm pull go-feature-flag/relay-proxy --untar -d charts/`), stamping `charts/relay-proxy/SOURCE.txt` with repo + exact chart version
- [x] 3.6 Write `deploy/values/relay-proxy.yaml` configuring GOFF's Kubernetes ConfigMap retriever (`kind: configmap`, …) — **superseded by section 10** (file retriever + volume mount)
- [x] 3.7 Rewrite the `demo-feature-flags` ConfigMap (`deploy/base/feature-flags-configmap.yaml`) from the old custom JSON into GOFF's flag format under a `flags.yaml` key (initially both `otel-nats-tracing` and `demo-nats-flow`; **section 9 removes the app flag** so only library flags remain; **section 10** updates comments for the ladder)
- [x] 3.8 Replace the relay-proxy ServiceAccount/Role/RoleBinding with ones bound to the GOFF chart's ServiceAccount for ConfigMap API reads — **superseded by section 10** (no ConfigMap API RBAC)
- [x] 3.9 Backend: register the OpenFeature GOFF provider at startup — later replaced by zero-code `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT`; set env tiers for revoke-only — **section 10** sets `OTEL_NATS_TRACING_ENABLED=false` for ladder option C

## 4. Frontend (JS)

- [x] 4.1 Scaffold a minimal browser app (`frontend/`) with `@opentelemetry/sdk-trace-web`, `FetchInstrumentation`, and OTLP HTTP exporter configured
- [x] 4.2 Implement a "Start Trace" button that triggers `POST /api/demo-trace` with `traceparent` injected via the fetch instrumentation
- [x] 4.3 Display the returned trace ID and a "View in Grafana" link (deep link using the trace ID) after a successful call
- [x] 4.4 Add a Dockerfile (static build served via a lightweight web server) for the frontend image

## 5. NATS deployment

- [x] 5.1 Vendor the official NATS Helm chart into `charts/nats` (`helm pull nats/nats --untar -d charts/`), stamping `charts/nats/SOURCE.txt` with the chart repo + version pulled
- [x] 5.2 Write `deploy/values/nats.yaml` enabling JetStream and sizing resources/replicas appropriately for a single-node `kind` cluster
- [x] 5.3 Add `helm install nats charts/nats -f deploy/values/nats.yaml -n demo` to the deploy tooling (see 7.6)

## 6. Observability pipeline (rotel + ClickHouse + Grafana)

- [x] 6.1 Pin a specific rotel image tag/version (resolve design.md's open question) and write its Deployment/Service plain manifest under `deploy/base/` (no official chart exists) with OTLP receiver endpoint exposed and ClickHouse exporter configured
- [x] 6.2 Vendor a ClickHouse Helm chart into `charts/clickhouse` (`helm pull ... --untar -d charts/`), stamping `charts/clickhouse/SOURCE.txt`; write `deploy/values/clickhouse.yaml` with persistent volume sized for `kind`, allowing rotel to create its default OTLP trace schema
- [x] 6.3 Vendor the official Grafana Helm chart into `charts/grafana`, stamping `charts/grafana/SOURCE.txt`; write `deploy/values/grafana.yaml` installing the `grafana-clickhouse-datasource` plugin
- [x] 6.4 Provision the ClickHouse data source automatically via `deploy/values/grafana.yaml`'s datasource provisioning section (no manual UI setup)
- [x] 6.5 Author a Grafana dashboard (JSON, provisioned automatically via the Grafana chart's dashboard-provisioning values) that looks up a trace by ID and renders its spans as a timeline/waterfall
- [x] 6.8 Extend that dashboard so the span-linked NATS spans are discoverable from the displayed trace ID, not silently omitted (design.md Decision 4: the async spans live under their own root trace IDs and are reachable only via `Links`) — e.g. a second panel querying `otel_traces` for spans whose links reference the entered trace ID
- [x] 6.6 Verify rotel readiness gating so frontend/backend OTLP exports don't fail hard before rotel is up (client-side retry or buffering)
- [x] 6.7 Add `helm install clickhouse charts/clickhouse -f deploy/values/clickhouse.yaml -n demo` and `helm install grafana charts/grafana -f deploy/values/grafana.yaml -n demo` to the deploy tooling (see 7.6)

## 7. Kubernetes deployment topology

- [x] 7.1 Write `deploy/kind-cluster.yaml` kind cluster config
- [x] 7.2 Write Kustomize base (`deploy/base/`) with `Namespace: demo` and the in-house component manifests only — NATS, ClickHouse, Grafana, and (per 3.5) the GOFF relay proxy are deployed via their vendored charts, not Kustomize. **Update for the rework**: drop `relay-proxy.yaml` from `resources`, keep the flags ConfigMap. (**Section 10** drops relay ConfigMap RBAC from the base / chart extras.)
- [x] 7.3 ~~ServiceAccount + Role + RoleBinding for the custom relay proxy~~ — reworked in 3.8 for GOFF API retriever; **section 10 removes** ConfigMap-read RBAC entirely (file mount)- [x] 7.4 ~~`demo-feature-flags` ConfigMap with custom JSON flag definitions~~ — reworked in 3.7 into GOFF flag format
- [x] 7.5 Add readiness/liveness probes to every in-house workload manifest (and confirm the vendored charts' default probes are enabled via `deploy/values/*.yaml`) so dependency-ordering tolerance (Requirement: "Component startup respects dependency ordering") holds regardless of apply order
- [x] 7.6 Write a `Makefile`/script target that builds all in-house images, `kind load docker-image`s them, runs the `helm install` commands, and runs `kubectl apply -k deploy/base`. **Update for the rework**: two in-house images (backend, frontend) not three, and four chart installs — NATS, ClickHouse, Grafana, GOFF relay proxy.
- [x] 7.7 Write a teardown target (`kind delete cluster`) as a single documented command
- [x] 7.8 Document `kubectl port-forward` access instructions for the frontend and Grafana in root `README.md`

## 8. End-to-end verification

- [x] 8.1 Bring up the full stack in `kind` and manually trigger the frontend demo flow
- [x] 8.2 Verify the trace ID returned to the frontend resolves in Grafana to a trace containing the frontend, backend HTTP, and NATS producer span (and historically flag-evaluation when app flags existed), and that the span-linked NATS `process` spans are discoverable from it (design.md Decision 4 — they are separate root traces by design, so a single-trace-ID query alone must NOT be treated as the pass criterion)
- [x] 8.3 ~~Verify editing `demo-nats-flow`…~~ — **superseded by section 9 / 9.6**: application flag verification removed; only library-flag verification remains
- [x] 8.4 Verify flipping `otel-nats-tracing` to disabled in the ConfigMap stops NATS spans being emitted while the NATS round trip still completes (and re-enabling restores spans) with no backend restart — the headline dynamic-instrumentation capability
- [x] 8.5 Verify the backend tolerates being deployed before NATS and before the relay proxy are ready (no crash-loop), and that with the relay unreachable instrumentation falls back to env-var defaults rather than erroring
- [x] 8.6 Run `openspec validate --strict` against this change and fix any reported issues

## 9. Library-only feature flags (remove app-side flag path)

> Scope refinement merged from the former `remove-app-feature-flags` change: the demo
> only needs to prove `instrumentation-go` / `instrumentation-js` library flags work.
> Application-level `demo-nats-flow` and related API/UI verification are removed.

- [x] 9.1 Backend: remove per-request evaluation of `demo-nats-flow`; always run the NATS round trip on a successful demo request when NATS is reachable
- [x] 9.2 Backend: slim `POST /api/demo-trace` success JSON to `{traceId, spanId}` only (drop `flagEnabled`, `natsFlowExecuted` and related span attributes)
- [x] 9.3 Backend: keep OpenFeature + GOFF provider setup for library resolution; remove app-only evaluation API from `internal/featureflags` if nothing else needs it; ensure NATS connect still does **not** use `WithTracingEnabled(...)`
- [x] 9.4 Backend tests: rewrite unit/integration tests that asserted app-flag skip/execute; keep coverage for library-flag behavior (NATS path still runs when tracing is disabled via in-memory/provider setup as applicable)
- [x] 9.5 Frontend: drop UI pills / type fields for `flagEnabled` and `natsFlowExecuted`; keep `traceId` display and Grafana link
- [x] 9.6 Deploy: remove `demo-nats-flow` from `deploy/base/feature-flags-configmap.yaml`; leave `otel-nats-tracing` (and comments that it is library-consumed only)
- [x] 9.7 Docs: update `README.md` and `README.zh-TW.md` — architecture, flag tables, and verification steps document **only** library-flag proof (flip `otel-nats-tracing` → spans on/off, business path still runs)
- [x] 9.8 Grafana: update any dashboard panel description that claims per-request `demo-nats-flow` evaluation
- [x] 9.9 Re-verify: happy path + live toggle of `otel-nats-tracing` only; run backend tests and `openspec validate --strict` for this change

## 10. Feature-flag ladder + ConfigMap volume mount

> Rework after `instrumentation-go` replaced the revoke-only kill switch with the
> four-step ladder (`relay > env > option > default`) and the demo chose posture
> **C** (module env explicitly `false`, relay enables). Also switch GOFF from the
> Kubernetes ConfigMap API retriever to a **volume-mounted** ConfigMap + `kind: file`.

- [x] 10.1 Patch vendored `charts/relay-proxy` Deployment/values to support `extraVolumes` / `extraVolumeMounts` (document the local patch in `SOURCE.txt` or a short NOTES comment)
- [x] 10.2 Rewrite `deploy/values/relay-proxy.yaml`: `retriever.kind: file`, `path: /flags/flags.yaml`; mount ConfigMap `demo-feature-flags` (key `flags.yaml`) at `/flags` with `optional: true`; keep short `pollingInterval` and `startWithRetrieverError: true`; **remove** Role/RoleBinding `extraManifests` for ConfigMap API reads
- [x] 10.3 Update `deploy/base/feature-flags-configmap.yaml` comments for ladder semantics (relay authoritative both ways; default variation stays `enabled`)
- [x] 10.4 Update `deploy/base/backend.yaml`: `OTEL_NATS_TRACING_ENABLED=false`, master switch still `1`, keep `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` + short poll interval; rewrite env comments for option C / zero-code provider path
- [x] 10.5 Backend tests: rewrite `TestRelayFlagTogglesNatsInstrumentation` with env falsy + relay toggle both ways; replace `TestRelayCannotEnableNatsInstrumentation` with `TestRelayEnablesWhatEnvLeftOff` (and keep a no-relay/env-off silent case if useful)
- [x] 10.6 Docs: update `README.md` / `README.zh-TW.md` — remove revoke-only / conjunctive-AND wording; document mount + ladder + option C verification steps
- [x] 10.7 Re-verify in cluster: default deploy emits NATS spans with env false; ConfigMap flip disables/re-enables spans without restart; run backend tests and `openspec validate --strict`
  - Verified on docker-desktop `demo` ns (2026-08-05): backend image with zero-code env + option C; GOFF file mount; happy path produces `send demo.trace.request`; after ConfigMap `variation: disabled` + ~70s kubelet/mount lag, NATS count 0 with HTTP still OK; re-enable restores spans. `go test ./...` green; `openspec validate --strict` green for this change and `align-demo-feature-flag-relay`.
