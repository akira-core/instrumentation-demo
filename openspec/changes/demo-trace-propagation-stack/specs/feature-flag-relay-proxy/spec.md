## Purpose

Provides a feature-flag relay proxy that sources its flag definitions from the cluster's own Kubernetes configuration rather than an external SaaS, and serves them over a standard flag-evaluation protocol so that both application code and the instrumentation library itself can be reconfigured at runtime without a restart.

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

### Requirement: Application behavior is gated on relay proxy evaluation
The backend SHALL evaluate a feature flag through the relay proxy during the demo flow and SHALL use the result to decide whether to execute the NATS round trip. That evaluation SHALL be part of the same trace as the rest of the request.

#### Scenario: Flag evaluation is visible in the demo trace
- **WHEN** the backend evaluates the demo flag while handling a demo request
- **THEN** the evaluation produces a span sharing the trace ID of that request's other synchronous spans

#### Scenario: Disabling the flag suppresses the gated behavior
- **WHEN** the demo flag is set to disabled and a demo request is made
- **THEN** the backend skips the NATS round trip, reports that it was skipped in its response, and still completes the request successfully

#### Scenario: Relay proxy unavailable does not fail the request
- **WHEN** the relay proxy cannot be reached during a demo request
- **THEN** the backend falls back to its configured default rather than returning an error to the caller

### Requirement: Instrumentation tracing is togglable at runtime via the relay proxy
The NATS instrumentation's own tracing SHALL be controlled by a flag served from the relay proxy, so that an operator can turn instrumentation on or off by editing the Kubernetes configuration source, with no application restart and no application code involved in the decision. Application configuration SHALL NOT pin the instrumentation to a static tracing state, since doing so would make the relay unable to affect it.

#### Scenario: Operator disables NATS tracing at runtime
- **WHEN** an operator sets the NATS tracing flag to disabled in the Kubernetes configuration source while the backend is running
- **THEN** subsequent demo requests produce no NATS instrumentation spans, without the backend being restarted

#### Scenario: Operator re-enables NATS tracing at runtime
- **WHEN** an operator sets that flag back to enabled
- **THEN** subsequent demo requests again produce NATS instrumentation spans, without the backend being restarted

#### Scenario: Environment default applies when the relay has no opinion
- **WHEN** the relay proxy is unreachable or does not define the NATS tracing flag
- **THEN** the instrumentation falls back to its environment-variable default rather than failing or entering an undefined state
