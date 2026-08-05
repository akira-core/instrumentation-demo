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

### Requirement: Library flags resolve through the instrumentation flag domain
Library feature flags (at minimum `otel-nats-tracing`) SHALL be resolved through the instrumentation OpenFeature domain used by the Go instrumentation libraries. Installing a provider only on the default OpenFeature slot SHALL NOT count as enabling relay control for those library flags.

#### Scenario: Default-slot provider is ignored for library flags
- **WHEN** a provider is bound only to the default OpenFeature slot and no provider is bound to the instrumentation flag domain and the zero-code endpoint variable is unset
- **THEN** library flag resolution does not treat the relay as available for instrumentation switches

#### Scenario: Instrumentation domain binding enables relay control
- **WHEN** a provider is bound to the instrumentation flag domain (or the zero-code endpoint variable is set so the library binds that domain)
- **THEN** subsequent library evaluations for defined instrumentation keys can receive the relay's configured values

### Requirement: Backend uses zero-code library flag wiring, not app gating
The backend SHALL point the instrumentation library at the relay via `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` (and optional poll-interval / API-key vars) so `otelnats` can resolve flags without the application installing an OpenFeature provider for that purpose. The backend SHALL NOT evaluate an application feature flag to decide whether to execute the NATS round trip; a successful demo request SHALL always attempt the NATS path.

#### Scenario: Library provider path without application default-slot install
- **WHEN** the backend starts under the standard deploy configuration
- **THEN** the process is configured with the library's relay endpoint environment variable and does not depend on an application call to install the default OpenFeature provider for library flags

#### Scenario: Demo request always runs NATS when healthy
- **WHEN** a demo request is handled successfully and NATS is reachable
- **THEN** the backend performs the NATS publish/consume/reply round trip without consulting an application-level feature flag

#### Scenario: Relay proxy unavailable does not fail the request
- **WHEN** the relay proxy cannot be reached during a demo request
- **THEN** the backend still completes the request using the local ladder below the relay rather than returning an error solely because flag evaluation failed

### Requirement: Deployment leaves module tracing off; relay enables it
The backend deployment SHALL set `OTEL_NATS_TRACING_ENABLED` to an explicit falsy value and SHALL keep the process-wide master switch enabled (`OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` truthy). The flags ConfigMap SHALL default `otel-nats-tracing` to enabled so out-of-the-box demo requests emit NATS instrumentation spans because the relay overrides the environment.

#### Scenario: Happy path spans come from the relay enable
- **WHEN** the stack is deployed with defaults and a demo request completes while the relay serves `otel-nats-tracing` as enabled
- **THEN** NATS instrumentation spans are produced even though `OTEL_NATS_TRACING_ENABLED` is falsy in the backend environment

#### Scenario: Env alone is off when the relay has no opinion
- **WHEN** the relay proxy is unreachable or does not define the NATS tracing flag
- **THEN** NATS instrumentation spans are not produced (the falsy module environment variable decides) and the request still succeeds

### Requirement: Flag resolution follows the library precedence ladder
Effective NATS tracing SHALL follow `relay > env > option > default`. The relay SHALL be authoritative in both directions. The process-wide master switch SHALL act as a veto only; a truthy master alone SHALL NOT enable module tracing.

#### Scenario: Relay disables tracing despite env that would enable
- **WHEN** a module environment value would enable NATS tracing and the relay serves `otel-nats-tracing` as disabled
- **THEN** subsequent demo requests complete the NATS business path but emit no NATS instrumentation spans without restarting the backend

#### Scenario: Relay enables tracing despite env disabled
- **WHEN** `OTEL_NATS_TRACING_ENABLED` is falsy and the relay serves `otel-nats-tracing` as enabled and the master switch is not vetoing
- **THEN** subsequent demo requests emit NATS instrumentation spans without restarting the backend

### Requirement: Instrumentation tracing is togglable at runtime via the relay proxy
An operator SHALL be able to turn NATS instrumentation on or off by editing the flags ConfigMap, with no application restart and no application code evaluating that flag on the request path, after provider and relay poll intervals elapse.

#### Scenario: Operator disables NATS tracing at runtime
- **WHEN** an operator sets the NATS tracing flag to disabled in the ConfigMap while the backend is running under the standard deploy configuration
- **THEN** subsequent demo requests still complete the NATS round trip but produce no NATS instrumentation spans without the backend being restarted

#### Scenario: Operator re-enables NATS tracing at runtime
- **WHEN** an operator sets that flag back to enabled
- **THEN** subsequent demo requests again produce NATS instrumentation spans without the backend being restarted

### Requirement: Flag configuration exposes instrumentation library flags only
The Kubernetes configuration source SHALL define the instrumentation library tracing flag(s) exercised by this demo (at minimum `otel-nats-tracing`). It SHALL NOT define application-level flags whose only purpose is to gate the demo's NATS business path from application code.

#### Scenario: ConfigMap lists library tracing flag
- **WHEN** an operator inspects the flag definitions in the Kubernetes configuration source
- **THEN** `otel-nats-tracing` is present with enabled/disabled variations

#### Scenario: No application NATS-flow gate flag
- **WHEN** an operator inspects the flag definitions in the Kubernetes configuration source
- **THEN** there is no application flag that the demo backend evaluates to skip or run the NATS round trip

### Requirement: Relay proxy serves a standard flag-evaluation protocol
The relay proxy SHALL expose flag evaluation over a protocol that a standard OpenFeature provider can consume, so that clients need no bespoke integration code.

#### Scenario: Evaluating a defined flag
- **WHEN** a client evaluates a flag key that is defined in the current configuration
- **THEN** the relay proxy returns that flag's configured value

#### Scenario: Evaluating an undefined flag
- **WHEN** a client evaluates a flag key that is not defined in the current configuration
- **THEN** the client receives its supplied default value rather than an error or an undefined state
