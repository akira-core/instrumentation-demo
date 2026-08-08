## MODIFIED Requirements

### Requirement: Library flags resolve through the instrumentation flag domain
Library feature flags (at minimum `otel-nats-tracing`) SHALL be resolved through the instrumentation OpenFeature domain belonging to the **runtime doing the resolving** — one domain per runtime, since a domain names a provider binding inside a single process. Installing a provider only on the default OpenFeature slot SHALL NOT count as enabling relay control for those library flags, in either runtime.

#### Scenario: Default-slot provider is ignored for library flags
- **WHEN** a provider is bound only to the default OpenFeature slot and no provider is bound to that runtime's instrumentation flag domain and the zero-code endpoint variable is unset
- **THEN** library flag resolution does not treat the relay as available for instrumentation switches

#### Scenario: Instrumentation domain binding enables relay control
- **WHEN** a provider is bound to that runtime's instrumentation flag domain (or the runtime's zero-code endpoint variable is set so the library binds that domain)
- **THEN** subsequent library evaluations for defined instrumentation keys can receive the relay's configured values

#### Scenario: Each runtime binds its own domain
- **WHEN** the Go backend and the JS service both resolve library flags against the relay
- **THEN** each binds its own runtime's instrumentation domain, and neither runtime's binding affects the other

#### Scenario: One relay serves both domains
- **WHEN** both runtimes evaluate the same library flag key against the relay proxy
- **THEN** both receive that key's configured value, because the flag key is a property of the configuration rather than of a domain

### Requirement: Instrumentation tracing is togglable at runtime via the relay proxy
An operator SHALL be able to turn NATS instrumentation on or off in **every** runtime the demo deploys by editing the flags ConfigMap, with no application restart and no application code evaluating that flag on the request path, after provider and relay poll intervals elapse. Each runtime's propagation bound is stated separately, since they differ.

#### Scenario: Operator disables NATS tracing at runtime
- **WHEN** an operator sets the NATS tracing flag to disabled in the ConfigMap while the demo is running under the standard deploy configuration
- **THEN** subsequent demo requests still complete the NATS round trip but produce no NATS instrumentation spans from the Go backend or the JS service, without either being restarted

#### Scenario: Operator re-enables NATS tracing at runtime
- **WHEN** an operator sets that flag back to enabled
- **THEN** subsequent demo requests again produce NATS instrumentation spans from both runtimes, without either being restarted

#### Scenario: Propagation bounds are documented per runtime
- **WHEN** an operator consults the documentation for how long a flip takes to take effect
- **THEN** the Go backend's bound (relay polling interval plus provider poll interval) and the JS service's bound (those plus its snapshot refresh interval) are each stated, rather than a single shared figure
