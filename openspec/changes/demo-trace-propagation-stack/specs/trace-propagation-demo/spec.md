## Purpose

Demonstrates W3C trace context propagating from a browser action through a Go backend and across a NATS publish/subscribe boundary, so that context propagation across a language and network boundary can be observed end-to-end and verified in a trace backend. Feature-flag verification for this stack targets **instrumentation library** flags only (see `feature-flag-relay-proxy`); the demo flow itself is not gated by application feature flags.

## ADDED Requirements

### Requirement: Frontend initiates a traced request
The frontend SHALL start a new trace when the user triggers the demo action, and SHALL inject a valid W3C `traceparent` header (and `tracestate` if present) into the HTTP request it sends to the backend.

#### Scenario: User triggers the demo flow
- **WHEN** the user clicks the "Start Trace" action in the frontend
- **THEN** the frontend sends an HTTP request to the backend's demo endpoint with a `traceparent` header containing a valid trace ID and a sampled flag

#### Scenario: Frontend surfaces the resulting trace identifiers
- **WHEN** the backend responds successfully to the demo request
- **THEN** the frontend displays the trace ID to the user in a human-readable form

### Requirement: Backend continues the trace across the HTTP boundary
The backend SHALL extract the incoming `traceparent`/`tracestate` context from the frontend's request and SHALL use it as the parent for the synchronous spans it creates while handling that request, including the NATS publish.

#### Scenario: Backend synchronous spans share the frontend's trace ID
- **WHEN** the backend receives a demo request carrying a `traceparent` header
- **THEN** the backend's HTTP server span and its NATS producer span (when NATS instrumentation tracing is enabled) SHARE the trace ID from the incoming `traceparent`

#### Scenario: Missing traceparent still produces a valid trace
- **WHEN** the backend receives a demo request with no `traceparent` header
- **THEN** the backend starts a new root trace rather than failing the request

#### Scenario: Separate requests produce separate traces
- **WHEN** two demo requests arrive without incoming trace context
- **THEN** each is assigned a distinct trace ID

### Requirement: Demo success response carries trace identifiers only
On a successful demo request, the backend SHALL return JSON that includes the active `traceId` and `spanId` for operator lookup in the observability backend. The response SHALL NOT include application feature-flag outcome fields (such as `flagEnabled` or `natsFlowExecuted`).

#### Scenario: Success payload is minimal
- **WHEN** the backend completes a successful demo request
- **THEN** the response body includes `traceId` and `spanId` and does not include `flagEnabled` or `natsFlowExecuted`

### Requirement: Trace context is carried in NATS message headers
The backend SHALL inject the active W3C trace context into the NATS message headers when publishing, and the NATS consumer SHALL extract that context from the received message, so that the causal relationship between publisher and consumer is recorded even though the two are asynchronous. A successful demo request SHALL always perform the NATS publish/consume/reply round trip when NATS is reachable (not gated by an application feature flag).

#### Scenario: Published message carries W3C trace headers
- **WHEN** the backend publishes the demo message to NATS while handling a traced request
- **THEN** the published message's headers contain a `traceparent` value derived from the request's active trace context

#### Scenario: Consumer span records the publisher as a span link
- **WHEN** a subscriber receives and processes that message and NATS instrumentation tracing is enabled
- **THEN** the subscriber's processing span is recorded with a consumer span kind AND carries a span link whose linked span context matches the publisher's producer span

#### Scenario: Reply round-trip completes the request
- **WHEN** the NATS consumer publishes a reply after processing
- **THEN** the original backend HTTP handler receives the reply and completes its server span only after the reply is observed

### Requirement: Asynchronous spans are linked rather than merged into the publisher's trace
Because the NATS boundary is asynchronous, the consumer's processing span SHALL be recorded as the root of its own trace and connected to the publisher via an OTel span link, rather than as a child sharing the publisher's trace ID. The demo SHALL NOT bypass or reimplement the instrumentation library's span-creation behavior in order to force a single trace ID.

#### Scenario: Consumer span is not a child of the producer span
- **WHEN** a demo flow completes and its NATS consumer span is inspected
- **THEN** the consumer span has no parent span, and its link — not a parent-child edge — is what identifies the producer

#### Scenario: The linked relationship is discoverable from the producer's trace
- **WHEN** an operator starts from the trace ID the frontend displayed
- **THEN** the NATS consumer activity is reachable from that trace via the recorded span link

### Requirement: Completed flows are queryable in the trace backend
Once a demo flow completes, its spans SHALL be retrievable from the observability backend, such that the synchronous portion is queryable by the trace ID surfaced to the user and the asynchronous portion is reachable from it via span links.

#### Scenario: Synchronous spans are retrievable by the displayed trace ID
- **WHEN** an operator queries the observability backend for the trace ID shown by the frontend after a completed demo flow with NATS instrumentation tracing enabled
- **THEN** the result includes the frontend request span, the backend HTTP server span, and the NATS producer span, all sharing that trace ID

#### Scenario: Asynchronous spans are retrievable and attributable
- **WHEN** an operator inspects the same completed flow's NATS processing spans
- **THEN** those spans are present in the trace backend, identify the originating component, and carry the link back to the producer span
