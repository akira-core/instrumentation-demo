## Purpose

Provides a feature-flag relay proxy that sources its flag definitions from a Kubernetes ConfigMap in the demo namespace (volume-mounted into the relay pod, not fetched via the Kubernetes API), and serves them over a standard flag-evaluation protocol so that **instrumentation libraries** can be reconfigured at runtime without a restart. The demo does not use application-level flags to gate business behavior. Flag resolution follows `instrumentation-go`'s four-step ladder: `relay > env > option > default`, with the relay authoritative in both directions.

## ADDED Requirements

### Requirement: Relay proxy sources flag definitions from a mounted ConfigMap
The relay proxy SHALL read its feature-flag definitions from a file path backed by a Kubernetes ConfigMap volume mount in its own namespace — not a static file baked into its image, and not an external SaaS — and SHALL reflect updates to that ConfigMap without being restarted. The relay SHALL NOT require Kubernetes API credentials to load those definitions.

#### Scenario: Relay proxy serves flags from mounted ConfigMap
- **WHEN** the relay proxy is running with the flags ConfigMap mounted
- **THEN** the flag values it serves are those defined in that ConfigMap

#### Scenario: Live update to ConfigMap is picked up without restart
- **WHEN** the flag definitions in the mounted ConfigMap are updated while the relay proxy is running
- **THEN** subsequent evaluations reflect the updated definition without the relay proxy process being restarted

#### Scenario: Relay does not need ConfigMap API RBAC
- **WHEN** the relay proxy's Kubernetes credentials (if any) are inspected
- **THEN** they do not grant read access to ConfigMaps for the purpose of loading flag definitions

### Requirement: Relay proxy serves a standard flag-evaluation protocol
The relay proxy SHALL expose flag evaluation over a protocol that a standard OpenFeature provider can consume, so that clients need no bespoke integration code.

#### Scenario: Evaluating a defined flag
- **WHEN** a client evaluates a flag key that is defined in the current configuration
- **THEN** the relay proxy returns that flag's configured value

#### Scenario: Evaluating an undefined flag
- **WHEN** a client evaluates a flag key that is not defined in the current configuration
- **THEN** the client receives its supplied default value rather than an error or an undefined state

### Requirement: Flag configuration exposes instrumentation library flags only
The Kubernetes configuration source SHALL define the instrumentation library tracing flag(s) exercised by this demo (at minimum `otel-nats-tracing` for Go `otelnats`). It SHALL NOT define application-level flags whose only purpose is to gate the demo's NATS business path from application code.

#### Scenario: ConfigMap lists library tracing flag
- **WHEN** an operator inspects the flag definitions in the Kubernetes configuration source
- **THEN** `otel-nats-tracing` is present with enabled/disabled variations

#### Scenario: No application NATS-flow gate flag
- **WHEN** an operator inspects the flag definitions in the Kubernetes configuration source
- **THEN** there is no application flag that the demo backend evaluates to skip or run the NATS round trip

### Requirement: Backend uses zero-code library flag wiring, not app gating
The backend SHALL point the instrumentation library at the relay via `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` (or equivalent documented zero-code wiring) so `otelnats` can resolve flags, and SHALL NOT pin NATS instrumentation with `otelnats.WithTracingEnabled(...)`. The backend SHALL NOT evaluate an application feature flag to decide whether to execute the NATS round trip; a successful demo request SHALL always attempt the NATS path. The backend need not install its own OpenFeature provider for the demo path.

#### Scenario: Library provider path without static tracing pin
- **WHEN** the backend starts and connects to NATS for the demo flow
- **THEN** the process is configured so the library can reach the relay, and the NATS connection is not constructed with a static tracing-enabled option that would bypass the ladder's relay and env rungs for demo purposes

#### Scenario: Demo request always runs NATS when healthy
- **WHEN** a demo request is handled successfully and NATS is reachable
- **THEN** the backend performs the NATS publish/consume/reply round trip without consulting an application-level feature flag

#### Scenario: Relay proxy unavailable does not fail the request
- **WHEN** the relay proxy cannot be reached during a demo request
- **THEN** the backend still completes the request using the instrumentation environment-variable and option ladder below the relay rather than returning an error solely because flag evaluation failed

### Requirement: Deployment leaves module tracing off; relay enables it
The backend deployment SHALL set `OTEL_NATS_TRACING_ENABLED` to an explicit falsy value and SHALL keep the process-wide master switch enabled (`OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` truthy). The flags ConfigMap SHALL default `otel-nats-tracing` to enabled so out-of-the-box demo requests emit NATS instrumentation spans only because the relay overrides the environment.

#### Scenario: Happy path spans come from the relay enable
- **WHEN** the stack is deployed with defaults and a demo request completes while the relay serves `otel-nats-tracing` as enabled
- **THEN** NATS instrumentation spans are produced even though `OTEL_NATS_TRACING_ENABLED` is falsy in the backend environment

#### Scenario: Env alone is off when the relay has no opinion
- **WHEN** the relay proxy is unreachable or does not define the NATS tracing flag
- **THEN** NATS instrumentation spans are not produced (the falsy module environment variable decides) and the request still succeeds

### Requirement: Instrumentation tracing is togglable at runtime via the relay proxy
The NATS instrumentation's own tracing SHALL be controlled by a flag served from the relay proxy under the library's precedence ladder, so that an operator can turn instrumentation on or off by editing the Kubernetes ConfigMap, with no application restart and no application code involved in the decision.

#### Scenario: Operator disables NATS tracing at runtime
- **WHEN** an operator sets the NATS tracing flag to disabled in the ConfigMap while the backend is running
- **THEN** subsequent demo requests still complete the NATS round trip as business behavior, but produce no NATS instrumentation spans, without the backend being restarted

#### Scenario: Operator re-enables NATS tracing at runtime
- **WHEN** an operator sets that flag back to enabled
- **THEN** subsequent demo requests again produce NATS instrumentation spans, without the backend being restarted
