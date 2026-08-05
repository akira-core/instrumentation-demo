## Context

Post-rebase demo branch already ships most of this alignment (see also `demo-trace-propagation-stack` Decision 5 and section 10). This change is the dedicated planning record for feature-flag relay alignment against `instrumentation-go` on `feat/inprogress-openfeature` (submodule gitlink with `otel-flags` + ladder).

Library facts that shape the design:

- Ladder: `relay > env > option > default`
- Zero-code: `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` (+ optional poll interval as a **Go duration**, API key)
- Named domain `otel-instrumentation-go`; default-slot providers are ignored for library keys
- Module default **off**; master is a **veto** (default on)
- No evaluation snapshot cache in `otel-flags` — toggle latency is GOFF poll + provider poll

See proposal.md for motivation.

## Goals / Non-Goals

**Goals:**
- Keep the kind demo proving live ConfigMap toggles of `otel-nats-tracing` (both directions) without restart.
- Prefer zero-code wiring; volume-mounted flags (no ConfigMap API RBAC).
- Option C deploy posture so happy-path spans prove **relay enable**, not env.

**Non-Goals:**
- Application-level demo flags.
- Changing NATS subject design, span-link semantics, or observability stack.
- Publishing instrumentation modules; continue submodule + `go.work`.

## Decisions

### 1. Zero-code path for the running demo

| Variable | Demo value | Role |
|---|---|---|
| `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` | `http://relay-proxy:1031` | Auto-install named GOFF provider |
| `OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL` | `2s` | Demo-responsive; must be a duration string |
| `OTEL_SERVICE_NAME` | `demo-backend` | Targeting on auto-install path |
| `OTEL_NATS_TRACING_ENABLED` | `false` | Option C local rung |
| `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` | `1` | Master veto open |

No `RELAY_PROXY_URL`, no `internal/featureflags` default-slot install.

### 2. GOFF file retriever + ConfigMap volume mount

```yaml
retriever:
  kind: file
  path: /flags/flags.yaml
```

Vendored chart supports `extraVolumes` / `extraVolumeMounts`. Mount is a directory (not `subPath`) so kubelet refreshes on edit. `optional: true` + `startWithRetrieverError: true` tolerate helm-before-k8s-apply.

### 3. Tests install on the named domain

`openfeature.SetNamedProviderAndWait("otel-instrumentation-go", …)` before NATS connect. Cover: toggle both ways with env false; relay enables what env left off; no-relay + env off emits no NATS spans.

### 4. Submodule workspace

`go.work` uses `./backend`, `./third_party/instrumentation-go/otel-flags`, `./third_party/instrumentation-go/otel-nats`. Docker copies both modules and recreates the same workspace.

## Risks / Trade-offs

- [Startup window before first provider fetch uses local env only] → Option C means that window is silent for NATS spans until fetch succeeds; document it.
- [Bare integer poll interval fails construction] → Only Go durations in manifests.
- [ConfigMap mount refresh is multi-second] → Document total latency (kubelet + GOFF 1s + provider 2s).

## Migration Plan

Already applied on the feature branch alongside `demo-trace-propagation-stack` §10. Remaining work is verification in kind and keeping this change's tasks/checklist honest.

## Open Questions

(none that block implementation — poll interval already chosen as `2s`.)
