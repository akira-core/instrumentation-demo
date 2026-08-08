## Purpose

Runs a Node service in the demo cluster that produces and consumes NATS messages through the JS instrumentation library, under the same relay-driven feature-flag ladder as the Go backend — so the demo proves that one ConfigMap flip governs library instrumentation across two runtimes, and that trace linking survives a language boundary.

## ADDED Requirements

### Requirement: A JS service participates in the demo NATS flow
The demo SHALL deploy a Node service that consumes and publishes NATS messages through the JS instrumentation library, exporting its spans to the same collector as the Go backend.

#### Scenario: JS service consumes the demo request subject
- **WHEN** a demo request causes the Go backend to publish on the demo request subject
- **THEN** the JS service receives that message and records having processed it

#### Scenario: JS service publishes and consumes its own subject
- **WHEN** the JS service has processed a demo request message
- **THEN** it publishes on its own demo subject and consumes that message as well, exercising both the producing and consuming sides of the library

#### Scenario: JS spans reach the same trace backend
- **WHEN** the JS service is tracing and has handled a message
- **THEN** its spans are queryable in the trace backend under its own service name, alongside the Go backend's spans

### Requirement: The JS service is additive to the existing Go flow
Adding the JS service SHALL NOT change the Go backend's messaging behaviour, its span output, or the outcome of a demo request.

#### Scenario: Go subscriber still receives every request message
- **WHEN** the JS service is running and subscribed to the demo request subject
- **THEN** the Go backend's own subscriber receives every message it received before, because the JS subscription does not share the Go subscriber's queue group

#### Scenario: Demo request outcome is unchanged
- **WHEN** a demo request is issued while the JS service is running
- **THEN** the request completes through the Go round trip exactly as it did without the JS service

#### Scenario: A failing JS service does not fail the demo
- **WHEN** the JS service is unavailable or crash-looping
- **THEN** demo requests still succeed and the Go backend's spans are unaffected

### Requirement: The JS service resolves tracing through the relay, with no application gating
The JS service SHALL leave its module tracing environment variable falsy, set the relay endpoint, and pass no per-connection tracing option, so any instrumentation it produces is explained solely by the deployment environment and the relay's flag value.

#### Scenario: Module environment leaves tracing off
- **WHEN** an operator inspects the JS service's deployment configuration
- **THEN** the module tracing variable is explicitly falsy and the master switch variable is truthy

#### Scenario: Relay endpoint is the whole of the wiring
- **WHEN** an operator inspects the JS service's deployment configuration and its source
- **THEN** the relay endpoint variable is set and the application installs no feature-flag provider and passes no tracing-enabled option of its own

#### Scenario: Spans appear only because the relay enabled them
- **WHEN** the flag's default variation is enabled and the JS service handles a message
- **THEN** JS instrumentation spans appear, which is explicable only by the relay enabling what the module environment left off

#### Scenario: Service name is targetable
- **WHEN** the JS service evaluates a flag
- **THEN** its evaluation carries its configured service name, so a relay rule can target this service specifically

### Requirement: One flag flip governs both runtimes
Editing the single NATS tracing flag in the Kubernetes configuration source SHALL change instrumentation in both the Go backend and the JS service, in both directions, without either being restarted.

#### Scenario: Disabling stops both runtimes
- **WHEN** an operator sets the NATS tracing flag to disabled and waits out both runtimes' propagation bounds
- **THEN** subsequent demo requests produce no NATS instrumentation spans from either the Go backend or the JS service

#### Scenario: Re-enabling restores both runtimes
- **WHEN** an operator sets that flag back to enabled and waits out both propagation bounds
- **THEN** subsequent demo requests again produce NATS instrumentation spans from both services

#### Scenario: Business path survives the disable
- **WHEN** the flag is disabled
- **THEN** the demo request still completes and the JS service still records having processed its message, so only instrumentation stopped

#### Scenario: Neither service restarts
- **WHEN** the flag is flipped in either direction
- **THEN** neither the Go backend's nor the JS service's pod restarts, and their restart counts are unchanged across the flip

#### Scenario: No additional flag key is required
- **WHEN** an operator inspects the flag definitions
- **THEN** the same single NATS tracing key governs both runtimes, with no JS-specific module key added

### Requirement: Trace context propagates across the language boundary
Trace context carried in NATS message headers SHALL be extracted by the JS consumer, and the resulting consumer span SHALL be linked to the producing span rather than parented to it.

#### Scenario: JS consumer span links to the Go producer span
- **WHEN** the JS service consumes a message the Go backend published while both are tracing
- **THEN** the JS consumer span is a root on its own trace carrying a span link to the Go producer's span context

#### Scenario: JS producer span propagates to the JS consumer
- **WHEN** the JS service publishes on its own subject and consumes that message
- **THEN** the consuming span carries a link to the publishing span, demonstrating propagation through headers the JS library both wrote and read

### Requirement: Readiness is independent of NATS and the relay
The JS service's liveness and readiness SHALL NOT depend on the NATS connection or on the feature-flag relay, so a flag flip or a messaging outage never restarts it.

#### Scenario: Ready before NATS connects
- **WHEN** the JS service starts while NATS is unavailable
- **THEN** it reports healthy, retries the connection in the background with backoff, and is not restarted by its probes

#### Scenario: Ready while the relay is unreachable
- **WHEN** the feature-flag relay is unavailable
- **THEN** the JS service reports healthy and continues handling messages with its locally-resolved flag values

### Requirement: Evidence capture covers both runtimes per campaign
The live evidence capture SHALL record, for each flag-flip campaign, the span outcome for both the Go backend and the JS service, and the rendered report SHALL present them together.

#### Scenario: Per-campaign JS span evidence is captured
- **WHEN** an evidence campaign runs
- **THEN** it writes a JS-service span query result alongside the existing Go-backend one, and the summary records a JS span count per campaign

#### Scenario: Existing evidence keys keep their meaning
- **WHEN** an evidence campaign runs after this change
- **THEN** every previously-existing artifact and summary key retains its name and measures the same thing, with the JS data added rather than substituted

#### Scenario: Disabled campaign shows both at zero
- **WHEN** the disabled campaign's evidence is inspected
- **THEN** both the Go backend's and the JS service's NATS span counts are zero while the demo request's response is successful

#### Scenario: One report shows both runtimes
- **WHEN** a reader opens the rendered flag-matrix report
- **THEN** a single report presents both runtimes' outcomes under each flag flip, rather than two separate reports

### Requirement: The service is built and deployed by the documented commands
The JS service SHALL be built, loaded, and deployed by the same top-level commands that build the existing services, and its readiness SHALL be waited on by the same command.

#### Scenario: Image builds with the other in-house images
- **WHEN** an operator runs the image-build command
- **THEN** the JS service's image is built alongside the backend and frontend images

#### Scenario: Image is loaded into the local cluster
- **WHEN** an operator runs the cluster image-load command against a kind cluster
- **THEN** the JS service's image is loaded with the others

#### Scenario: Deployment applies with the base
- **WHEN** an operator applies the in-house service manifests
- **THEN** the JS service's Deployment is created along with the existing services

#### Scenario: Readiness is waited on
- **WHEN** an operator runs the wait-for-ready command
- **THEN** it waits for the JS service's rollout in addition to the existing deployments
