## Purpose

Provides a feature-flag relay proxy that sources its flag definitions from the cluster's own Kubernetes configuration rather than an external SaaS, and serves them over a standard flag-evaluation protocol so that **instrumentation libraries** can be reconfigured at runtime without a restart. The demo does not use application-level flags to gate business behavior.

## ADDED Requirements

### Requirement: Relay proxy sources flag definitions from Kubernetes config
The relay proxy SHALL read its feature-flag definitions from a Kubernetes-native configuration source in its own cluster/namespace — not a static file baked into its image, and not an external service — and SHALL reflect updates to that source without being restarted.

#### Scenario: Relay proxy serves flags from cluster config
- **WHEN** the relay proxy is running in the cluster
- **THEN** the flag values it serves are those defined in the Kubernetes configuration source

#### Scenario: Live update to cluster config is picked up without restart
- **WHEN** the flag definitions in the Kubernetes configuration source are updated while the relay proxy is running
- **THEN** subsequent evaluations reflect the updated definition without the relay proxy process being restarted

### Requirement: Relay proxy access to Kubernetes config is least-privilege
The relay proxy's Kubernetes credentials SHALL grant only the read access needed to load flag definitions, scoped to its own namespace and to the specific configuration resource holding those definitions.

#### Scenario: Credentials are namespace- and resource-scoped
- **WHEN** the relay proxy's Kubernetes role is inspected
- **THEN** it grants only read verbs, only within the demo namespace, and only for the named flag-definition resource — not cluster-wide, not write access, and not all resources of that type

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

### Requirement: Backend installs OpenFeature for libraries, not app gating
The backend SHALL register a process-global OpenFeature provider pointed at the relay proxy at startup so instrumentation modules can resolve flags, and SHALL NOT pin NATS instrumentation to a static tracing state (for example via `otelnats.WithTracingEnabled(...)`). The backend SHALL NOT evaluate an application feature flag to decide whether to execute the NATS round trip; a successful demo request SHALL always attempt the NATS path.

#### Scenario: Provider is installed without static tracing pin
- **WHEN** the backend starts and connects to NATS for the demo flow
- **THEN** the OpenFeature provider is configured against the relay proxy and the NATS connection is not pinned with a static tracing-enabled option that would bypass OpenFeature

#### Scenario: Demo request always runs NATS when healthy
- **WHEN** a demo request is handled successfully and NATS is reachable
- **THEN** the backend performs the NATS publish/consume/reply round trip without consulting an application-level feature flag

#### Scenario: Relay proxy unavailable does not fail the request
- **WHEN** the relay proxy cannot be reached during a demo request
- **THEN** the backend still completes the request using instrumentation environment-variable defaults rather than returning an error solely because flag evaluation failed

### Requirement: Instrumentation tracing is togglable at runtime via the relay proxy
The NATS instrumentation's own tracing SHALL be controlled by a flag served from the relay proxy, so that an operator can turn instrumentation on or off by editing the Kubernetes configuration source, with no application restart and no application code involved in the decision. Application configuration SHALL NOT pin the instrumentation to a static tracing state, since doing so would make the relay unable to affect it.

#### Scenario: Operator disables NATS tracing at runtime
- **WHEN** an operator sets the NATS tracing flag to disabled in the Kubernetes configuration source while the backend is running
- **THEN** subsequent demo requests still complete the NATS round trip as business behavior, but produce no NATS instrumentation spans, without the backend being restarted

#### Scenario: Operator re-enables NATS tracing at runtime
- **WHEN** an operator sets that flag back to enabled
- **THEN** subsequent demo requests again produce NATS instrumentation spans, without the backend being restarted

#### Scenario: Environment default applies when the relay has no opinion
- **WHEN** the relay proxy is unreachable or does not define the NATS tracing flag
- **THEN** the instrumentation falls back to its environment-variable default rather than failing or entering an undefined state
