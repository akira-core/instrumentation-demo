## 1. Module resolution

- [x] 1.1 Ensure submodule `third_party/instrumentation-go` is checked out at the parent gitlink commit that includes `otel-flags` (tracked branch `feat/inprogress-openfeature`)
- [x] 1.2 Ensure root `go.work` lists `backend`, `otel-flags`, and `otel-nats` submodule paths
- [x] 1.3 Ensure `backend/Dockerfile` copies `otel-flags` + `otel-nats` and inits the same three-member workspace

## 2. Deploy: zero-code + volume-mounted relay

- [x] 2.1 Backend Deployment: `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT`, short `OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL` (Go duration), option C env (`OTEL_NATS_TRACING_ENABLED=false`, master truthy); no `RELAY_PROXY_URL`
- [x] 2.2 Flags ConfigMap comments document ladder + option C; only `otel-nats-tracing`
- [x] 2.3 GOFF values: file retriever at `/flags/flags.yaml`, volume mount of `demo-feature-flags`, no ConfigMap API Role; short `pollingInterval`
- [x] 2.4 Vendored chart supports `extraVolumes` / `extraVolumeMounts` if not upstream

## 3. Backend application wiring

- [x] 3.1 No application OpenFeature provider install for library flags (zero-code only)
- [x] 3.2 Config reports flags endpoint for logs only; does not invent a default that disagrees with the library
- [x] 3.3 NATS connect does not pin tracing via a demo-only static option that muddies ladder verification

## 4. Tests

- [x] 4.1 Install in-memory provider on named domain `otel-instrumentation-go`
- [x] 4.2 Toggle both directions with env falsy; relay-enables-what-env-left-off; no-relay env-off silent
- [x] 4.3 Run `go test ./...` under the workspace

## 5. Docs and verification

- [x] 5.1 README / README.zh-TW document ladder, option C, mount, poll latency
- [x] 5.2 Re-verify in kind when a cluster is available (happy path + ConfigMap flip)
- [x] 5.3 `openspec validate align-demo-feature-flag-relay --strict`
