## 1. Submodule and workspace

- [ ] 1.1 Bump the `third_party/instrumentation-js` gitlink to a commit where `add-dynamic-feature-flags` has landed, and update `.gitmodules`' tracked branch to match
- [ ] 1.2 Add `js-service` to the root `pnpm-workspace.yaml` alongside `frontend`, and link `@akira-core/otel-nats` / `@akira-core/otel-flags` from the submodule workspace
- [ ] 1.3 Confirm both packages build from the submodule checkout before wiring anything into the image

## 2. The service

- [ ] 2.1 Scaffold `js-service/` (`package.json`, `tsconfig.json`, `src/`), Node 22, ESM, TypeScript compiled ahead of run
- [ ] 2.2 Implement OTel bootstrap: `NodeTracerProvider` with an OTLP/HTTP exporter pointed at `OTEL_EXPORTER_OTLP_ENDPOINT`, resource carrying `service.name` from `OTEL_SERVICE_NAME`, W3C propagator; no sampler override, so the demo's default sampling matches the Go backend's
- [ ] 2.3 Implement the NATS lifecycle: connect via `@akira-core/otel-nats` with retry and exponential backoff in the background, never blocking startup; pass **no** `tracingEnabled` option, mirroring `backend/internal/natsflow`'s comment about not muddying ladder verification
- [ ] 2.4 Subscribe to `demo.trace.request` with **no** queue group; on each message log the correlation header at info and publish to `demo.trace.js`, continuing the consumed message's context
- [ ] 2.5 Subscribe to `demo.trace.js` and log receipt, so the service exercises the consuming side of a message its own library produced
- [ ] 2.6 Implement a minimal HTTP listener exposing a single `/healthz` that never blocks on NATS or the relay, serving both the liveness and the readiness probe — mirroring the Go backend, whose probes both key on `/healthz`
- [ ] 2.7 Handle SIGTERM: drain the NATS connection, shut down the tracer provider, and call the flags package's shutdown so the process exits promptly
- [ ] 2.8 Write `js-service/Dockerfile` building from the repo root (so `third_party/instrumentation-js` is in context), multi-stage, shipping `node_modules` via `pnpm deploy --prod` rather than a bundle — per design.md's WASM-loading decision

## 3. Deploy

- [ ] 3.1 Write `deploy/base/js-service.yaml`: Deployment (1 replica, image `demo-js-service:local`, `imagePullPolicy: IfNotPresent`), liveness/readiness on `/healthz`, resource requests in line with the backend's
- [ ] 3.2 Set the env block with the same option-C posture as `backend.yaml`, commented the same way: `NATS_URL`, `OTEL_EXPORTER_OTLP_ENDPOINT=http://rotel:4318`, `OTEL_SERVICE_NAME=demo-js-service`, `OTEL_INSTRUMENTATION_JS_TRACING_ENABLED=1`, `OTEL_NATS_TRACING_ENABLED=false`, `OTEL_INSTRUMENTATION_JS_FLAGS_ENDPOINT=http://relay-proxy:1031`, `OTEL_INSTRUMENTATION_JS_FLAGS_POLL_INTERVAL=2s`
- [ ] 3.3 Add `js-service.yaml` to `deploy/base/kustomization.yaml`
- [ ] 3.4 Update `deploy/base/feature-flags.yaml`'s comment block: the same `otel-nats-tracing` key now governs two runtimes, and the two propagation bounds differ. No key or variation changes
- [ ] 3.5 Makefile: add the image to `build-images` and `kind-load`, add the rollout to `wait-ready`
- [ ] 3.6 Verify `make deploy` on a fresh kind cluster brings all four in-house workloads to Ready

## 4. Verification in the cluster

- [ ] 4.1 Happy path: issue a demo request, confirm the Go round trip completes unchanged and both services' NATS spans are queryable in ClickHouse
- [ ] 4.2 Confirm the Go backend's span counts for a single request match the pre-change evidence, proving the ungrouped JS subscription stole nothing
- [ ] 4.3 Confirm the JS consumer span is a root carrying a link to the Go producer's span context, not a child of it
- [ ] 4.4 Flip the ConfigMap to `disabled`; after both propagation bounds, confirm both runtimes emit no NATS spans, the demo request still succeeds, and the JS service still logs a consumed message
- [ ] 4.5 Flip back to `enabled`; confirm both runtimes resume
- [ ] 4.6 Confirm neither pod's restart count changed across both flips
- [ ] 4.7 Confirm the JS service stays Ready with the relay scaled to zero, and recovers when it returns

## 5. Evidence and reporting

- [ ] 5.1 Extend `docs/scripts/capture-live-evidence.sh`: per campaign, add a ClickHouse query filtered to `ServiceName = 'demo-js-service'` written to `live-step-c<N>-clickhouse-js.txt`, and add a JS span count per campaign to `live-summary.json` — additively, leaving every existing key's name and meaning intact
- [ ] 5.2 Derive the script's post-flip wait from the larger of the two runtimes' propagation bounds, and record which bound was used in the capture log
- [ ] 5.3 Capture the JS service's env and pod state into the baseline artifacts alongside the backend's
- [ ] 5.4 Extend `docs/scripts/render-flag-matrix-html.py` to render both runtimes in one report — a column per runtime, not a second report
- [ ] 5.5 Regenerate `docs/evidence/` and both rendered HTML reports in a single commit so no reader sees a half-updated set
- [ ] 5.6 Run the capture script end to end on a live cluster and confirm it exits clean

## 6. Docs

- [ ] 6.1 `README.md`: correct the component table's claim that the JS library is only "available in-tree"; add the JS service to the architecture diagram and the component list
- [ ] 6.2 `README.md`: document the two-runtime flag flip, stating each runtime's propagation bound separately, and note the `demo.trace.js` self-loop is for demonstration rather than a realistic service boundary
- [ ] 6.3 `README.zh-TW.md`: mirror 6.1 and 6.2
- [ ] 6.4 Note in the README that the metrics pipeline's NATS message-count series steps up when this lands, since the request subject now has two deliveries per message
- [ ] 6.5 Run `openspec validate add-js-nats-service --strict`
- [ ] 6.6 Archive `align-demo-feature-flag-relay` **before** archiving this change: `feature-flag-relay-proxy` is still a delta in `changes/` rather than a main spec, and this change's `MODIFIED` requirements have nothing to modify until it lands in `openspec/specs/`
