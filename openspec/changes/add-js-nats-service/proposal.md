## Why

The README's own component table admits the gap: `@akira-core/otel-nats` is listed as "available in-tree for JS NATS work; this UI demo focuses on the HTTP→Go→NATS path". The JS instrumentation library is a submodule that nothing in the cluster runs. Everything the demo proves about the feature-flag ladder — that an operator can enable and disable library instrumentation on a running process, from a ConfigMap, with no application code and no restart — is proved for Go only.

That was accurate while `@akira-core/otel-nats` had no relay support. `instrumentation-js`'s `add-dynamic-feature-flags` gives it the same ladder, resolving the **same** `otel-nats-tracing` key against the **same** relay proxy. The demo is where that claim gets tested against a real relay, a real NATS server, and a real trace backend rather than against a test container — which is what this stack exists for.

A second, less obvious gap closes with it. `demo-trace-propagation-stack` records that async NATS spans are linked roots rather than children, and the evidence files show a completed run producing several trace IDs. That property is currently observed only within one runtime. A JS consumer subscribed to the same subject shows the same linking behaviour across a language boundary, which is the case a polyglot deployment actually has.

## What Changes

- Add a Node service (`js-service/`) to the demo: a NATS subscriber and publisher built on `@akira-core/otel-nats` and `@akira-core/otel-flags`, exporting spans to the same `rotel` collector as the Go backend.
- Topology, chosen so the Go backend's behaviour and its existing evidence stay reproducible byte for byte: the JS service subscribes to `demo.trace.request` **without a queue group**, so it receives every message alongside the Go backend's existing subscriber rather than stealing from it; it then publishes to a new subject `demo.trace.js`, which it also subscribes to. No Go code changes.
- Deploy it with the same "option C" flag posture the Go backend uses — master truthy, `OTEL_NATS_TRACING_ENABLED=false`, relay endpoint set — so the happy path proves a relay **enable** and the ConfigMap flip proves both directions, for both languages, from one key.
- Extend `docs/scripts/capture-live-evidence.sh` so each flag-flip campaign asserts on **both** services' spans, and the rendered HTML report shows them side by side.
- Wire the service into `make build-images`, `make kind-load`, `make wait-ready`, and the Kustomize base.

## Capabilities

### New Capabilities
- `js-nats-demo-service`: A Node service in the demo cluster that produces and consumes NATS messages through the JS instrumentation library, under the same relay-driven flag ladder as the Go backend.

### Modified Capabilities
- `feature-flag-relay-proxy`: runtime toggling is now proved for both runtimes from one flag key, and flag resolution now involves one instrumentation OpenFeature domain **per runtime** rather than a single domain.

## Impact

- **New code**: `js-service/` (`package.json`, `src/main.ts`, `Dockerfile`, `tsconfig.json`), `deploy/base/js-service.yaml`.
- **Modified**: `deploy/base/kustomization.yaml`, `deploy/base/feature-flags.yaml` (comments only — the key set does not change), `Makefile` (`build-images`, `kind-load`, `wait-ready`, `port-forward` help text), `docs/scripts/capture-live-evidence.sh`, `docs/scripts/render-flag-matrix-html.py`, `README.md`, `README.zh-TW.md`, root `pnpm-workspace.yaml`.
- **Unmodified by design**: `backend/`, `deploy/base/backend.yaml`, and every existing evidence artifact's meaning. The new subscriber is additive; the Go round trip runs exactly as before.
- **Submodule dependency**: requires `third_party/instrumentation-js` at a commit where `add-dynamic-feature-flags` has landed, tracked as a gitlink bump the same way `third_party/instrumentation-go` tracks `feat/inprogress-openfeature`.
- **New flag definitions**: none. The whole point is that `otel-nats-tracing` already there governs both runtimes.
- **Cluster cost**: one more small Deployment (single replica, no persistence, no port-forward needed).
