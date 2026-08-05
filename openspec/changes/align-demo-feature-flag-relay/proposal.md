## Why

`instrumentation-go` now resolves instrumentation switches through a published `otel-flags` layer and a four-step ladder (`relay > env > option > default`), with zero-code relay wiring via `OTEL_INSTRUMENTATION_GO_FLAGS_*` and a named OpenFeature domain. After rebasing the demo branch, the planning record for that alignment must match the shipped design: volume-mounted GOFF flags, option-C deploy posture (module env off, relay enables), and no application-owned default-slot provider.

## What Changes

- Align the demo with the library ladder so ConfigMap flips of `otel-nats-tracing` enable **and** disable NATS spans without restart.
- Wire the backend with the **zero-code** path (`OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` + short Go-duration poll interval); remove app-owned `SetProvider` / `RELAY_PROXY_URL` provider install.
- Source GOFF flags from a **volume-mounted** ConfigMap (`retriever.kind: file`), not the Kubernetes API ConfigMap retriever (no flag RBAC).
- Deploy posture **option C**: `OTEL_NATS_TRACING_ENABLED=false`, master switch open, ConfigMap default `otel-nats-tracing=enabled`.
- Resolve `otel-nats` + `otel-flags` from the `instrumentation-go` submodule (`go.work` / Docker workspace).
- Tests bind the named domain `otel-instrumentation-go` and cover both ladder directions.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `feature-flag-relay-proxy`: Ladder semantics, zero-code endpoint wiring, volume-mounted flag source, option-C env posture, runtime enable/disable via ConfigMap.

## Impact

- **Backend**: env-only relay wiring; tests on named OpenFeature domain; submodule `otel-flags` in workspace/Docker.
- **Deploy**: backend env vars; flags ConfigMap comments; GOFF values (file retriever + volume mounts); vendored chart volume hooks.
- **Docs**: README ladder / toggle / poll-latency story.
- **No change** to frontend UX, NATS subjects, span-link topology, or observability pipeline shape.
