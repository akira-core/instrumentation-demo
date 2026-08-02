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
- [x] 2.3 ~~Implement relay-proxy client call (`GET /evaluate/demo-nats-flow`) as a CLIENT span~~ — **superseded by 3.4/3.5**: the bespoke HTTP client is replaced by an OpenFeature evaluation against the GOFF relay proxy (design.md Decision 5)
- [x] 2.4 ~~Wire `otelnats.ConnectWithOptions(..., WithTracingEnabled(true))`~~ — **superseded by 3.6**: that option makes the connection fully static and blocks every relay-driven flag change, defeating the demo's headline feature. Publishing to `demo.trace.request` with the active context is unchanged.
- [x] 2.5 Implement in-process subscriber on `demo.trace.request` (CONSUMER span via `otelnats.Subscribe`) that does trivial work and publishes a reply on `demo.trace.reply`
- [x] 2.6 Wire the original handler to await the reply, close its SERVER span, and return `{traceId, spanId}` JSON
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
- [x] 3.6 Write `deploy/values/relay-proxy.yaml` configuring GOFF's Kubernetes ConfigMap retriever (`kind: configmap`, `namespace: demo`, `configmap: demo-feature-flags`, `key: flags.yaml`), a short `pollingInterval` so `kubectl edit` shows up quickly, and modest `kind`-sized resources
- [x] 3.7 Rewrite the `demo-feature-flags` ConfigMap (`deploy/base/feature-flags-configmap.yaml`) from the old custom JSON into GOFF's flag format under a `flags.yaml` key, defining both `otel-nats-tracing` (library-consumed) and `demo-nats-flow` (application-consumed), each with `variations` + a `defaultRule`
- [x] 3.8 Replace the relay-proxy ServiceAccount/Role/RoleBinding with ones bound to the GOFF chart's ServiceAccount, scoped to `get`/`list`/`watch` on the single `demo-feature-flags` ConfigMap in the `demo` namespace
- [x] 3.9 Backend: register the OpenFeature GOFF provider at startup — `gofeatureflag.NewProvider(ProviderOptions{Endpoint: "http://relay-proxy:1031", EvaluationType: REMOTE, DisableCache: true, HTTPClient: <otelhttp-instrumented>})` + `openfeature.SetProviderAndWait` — remove `otelnats.WithTracingEnabled(true)` from the NATS connect call, delete `internal/relayclient`, evaluate `demo-nats-flow` through the OpenFeature client, and set `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=1` + `OTEL_NATS_TRACING_ENABLED=1` in the backend manifest (the env-only kill switch must be on for any relay value to apply, and the env var is the fallback default)

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
- [x] 7.2 Write Kustomize base (`deploy/base/`) with `Namespace: demo` and the in-house component manifests only — NATS, ClickHouse, Grafana, and (per 3.5) the GOFF relay proxy are deployed via their vendored charts, not Kustomize. **Update for the rework**: drop `relay-proxy.yaml` from `resources`, keep the flags ConfigMap and the relay proxy's RBAC there.
- [x] 7.3 ~~ServiceAccount + Role + RoleBinding for the custom relay proxy~~ — reworked in 3.8 to bind the GOFF chart's ServiceAccount instead
- [x] 7.4 ~~`demo-feature-flags` ConfigMap with custom JSON flag definitions~~ — reworked in 3.7 into GOFF flag format
- [x] 7.5 Add readiness/liveness probes to every in-house workload manifest (and confirm the vendored charts' default probes are enabled via `deploy/values/*.yaml`) so dependency-ordering tolerance (Requirement: "Component startup respects dependency ordering") holds regardless of apply order
- [x] 7.6 Write a `Makefile`/script target that builds all in-house images, `kind load docker-image`s them, runs the `helm install` commands, and runs `kubectl apply -k deploy/base`. **Update for the rework**: two in-house images (backend, frontend) not three, and four chart installs — NATS, ClickHouse, Grafana, GOFF relay proxy.
- [x] 7.7 Write a teardown target (`kind delete cluster`) as a single documented command
- [x] 7.8 Document `kubectl port-forward` access instructions for the frontend and Grafana in root `README.md`

## 8. End-to-end verification

- [x] 8.1 Bring up the full stack in `kind` and manually trigger the frontend demo flow
- [x] 8.2 Verify the trace ID returned to the frontend resolves in Grafana to a trace containing the frontend, backend HTTP, and flag-evaluation spans plus the NATS producer span, and that the span-linked NATS `process` spans are discoverable from it (design.md Decision 4 — they are separate root traces by design, so a single-trace-ID query alone must NOT be treated as the pass criterion)
- [x] 8.3 Verify editing `demo-nats-flow` in the `demo-feature-flags` ConfigMap changes the backend's `natsFlowExecuted` result on the next request, with neither the relay proxy nor the backend restarted
- [x] 8.4 Verify flipping `otel-nats-tracing` to disabled in the same ConfigMap stops NATS spans being emitted (and re-enabling restores them) with no backend restart — the headline dynamic-instrumentation capability of the `feat/inprogress-openfeature` branch
- [x] 8.5 Verify the backend tolerates being deployed before NATS and before the relay proxy are ready (no crash-loop), and that with the relay unreachable it falls back to its env-var defaults rather than erroring
- [x] 8.6 Run `openspec validate --strict` against this change and fix any reported issues
