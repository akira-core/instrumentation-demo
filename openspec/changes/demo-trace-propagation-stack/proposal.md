## Why

This repo currently has no working demo of distributed trace propagation, and the sibling packages (`@akira-core/otel-nats` in `instrumentation-js`, `otelnats`/`oteljetstream`/`otel-mongo`/`otel-gorilla-ws` in `instrumentation-go`) have no end-to-end reference application proving they interoperate across a network boundary and a language boundary. We need a runnable, multi-component demo showing a single W3C trace flow from a JS frontend click through a Go backend, across NATS, into an OTel collector, stored in ClickHouse, and visualized in Grafana — plus a feature-flag relay proxy sourced from Kubernetes config, to demonstrate the OpenFeature-relay pattern already used for dynamic tracing toggles in `instrumentation-go`.

## What Changes

- Add a JS frontend (browser app) that starts a trace, calls the Go backend over HTTP with `traceparent`/`tracestate` propagated, and displays the resulting trace ID.
- Add a Go backend service that receives the HTTP request, continues the trace, publishes a NATS message (via `otelnats`) carrying the propagated context, and consumes the reply/downstream message to close the loop.
- Add a NATS deployment (single-node, JetStream enabled) as the message bus between backend components.
- Deploy the upstream **GO Feature Flag (GOFF) relay proxy**, configured with its native Kubernetes ConfigMap retriever so flag definitions come from cluster config rather than an external SaaS. The backend registers the OpenFeature GOFF provider at startup so **instrumentation libraries** (e.g. `otelnats`'s `otel-nats-tracing`) resolve flags at runtime; an operator toggles library tracing by editing a ConfigMap — no restart. The demo does **not** use application-level flags to gate business behavior (NATS always runs on a successful demo request); the sole flag verification target is library instrumentation.
- Add an observability pipeline: `rotel` (OTel collector) receiving OTLP from frontend + backend, exporting spans into ClickHouse, with Grafana dashboards querying ClickHouse to visualize the full trace.
- Add Kubernetes manifests, a unified `charts/` directory of vendored third-party Helm charts (NATS, ClickHouse, Grafana), and a `kind` cluster config to run every component (frontend, backend, NATS, relay proxy, rotel, ClickHouse, Grafana) locally in a single cluster, with the relay proxy reading its config via the in-cluster Kubernetes API (ServiceAccount + RBAC, not a mounted static file).
- **New repo scaffolding**: this is currently an empty repo (README stub only) — this change bootstraps `frontend/`, `backend/`, `deploy/`, `charts/` (k8s manifests + kind config + vendored third-party charts), and wiring to consume `@akira-core/otel-nats` and `otelnats`/`oteljetstream` from the sibling repos via git submodules.

## Capabilities

### New Capabilities
- `trace-propagation-demo`: End-to-end behavior contract for how a single trace flows frontend → backend (HTTP) → NATS (publish/consume) → OTel collector, including which spans/kinds are expected and how trace/span IDs are surfaced back to the user for verification.
- `feature-flag-relay-proxy`: Behavior of the relay proxy — how it loads/watches flag config from Kubernetes, the OpenFeature evaluation protocol it exposes, and how **instrumentation** tracing (`otel-nats-tracing`) is toggled at runtime without application code evaluating app-level flags.
- `observability-stack`: Behavior of the rotel → ClickHouse → Grafana pipeline — what OTLP data is accepted, how it's persisted in ClickHouse, and what the provisioned Grafana dashboard(s) must show.
- `kind-cluster-deployment`: Deployment topology and operational contract — what `kind` cluster config and k8s manifests must exist, startup/dependency ordering, and how a developer brings the whole stack up and tears it down locally.

### Modified Capabilities
(none — greenfield repo, no existing specs)

## Impact

- **New code**: `frontend/` (browser app using `@opentelemetry/sdk-trace-web` + fetch instrumentation) and `backend/` (Go service depending on `otelnats`, plus the OpenFeature Go SDK and the GOFF provider). No bespoke relay-proxy service — the demo runs the upstream GOFF relay proxy, which is exactly what `instrumentation-go`'s OpenFeature integration is built to talk to.
- **New infra**: `deploy/kind-cluster.yaml`, a Kustomize base for in-house services (frontend, backend, relay-proxy, RBAC, ConfigMap, rotel), a top-level `charts/` directory of vendored third-party Helm charts (NATS, ClickHouse, Grafana) with per-chart `deploy/values/*.yaml`, RBAC for the relay proxy's Kubernetes API access, rotel collector config, Grafana provisioning (datasource + dashboard JSON) via chart values.
- **Cross-repo dependency**: consumes `instrumentation-js` and `instrumentation-go` as git submodules under `third_party/`, each tracking a branch (not pinned to a fixed commit) so the user controls when to advance them — `go.work` and the frontend's package workspace reference the submodule paths directly for both local dev and container builds.
- **No impact** on the sibling repos themselves — this change is additive and scoped entirely to `instrumentation-demo`.
