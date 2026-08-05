## Purpose

Collects, stores, and visualizes the trace data produced by the demo so that a completed trace-propagation flow can be inspected visually, not just verified programmatically.

## ADDED Requirements

### Requirement: Collector accepts OTLP from frontend and backend
The observability pipeline SHALL accept OTLP trace data submitted by both the frontend and the backend, over a network-reachable endpoint within the cluster.

#### Scenario: Frontend spans are accepted
- **WHEN** the frontend exports its collected spans via OTLP
- **THEN** the collector accepts them without error

#### Scenario: Backend spans are accepted
- **WHEN** the backend exports its collected spans (HTTP, flag evaluation, NATS producer/consumer) via OTLP
- **THEN** the collector accepts them without error

### Requirement: Collected spans are persisted for querying
Spans accepted by the collector SHALL be persisted to a queryable store such that a complete trace can be retrieved by trace ID after the originating request has finished.

#### Scenario: Trace is queryable shortly after completion
- **WHEN** a demo flow completes and the collector has processed all its spans
- **THEN** querying the store by that trace's ID returns all spans belonging to it within a short, bounded delay

### Requirement: Visualization surfaces the trace and its linked spans
The dashboarding tool SHALL provide a view where a single trace ID can be looked up and its spans displayed as a timeline (or equivalent trace waterfall), showing span names, service/component, and duration. Because the demo's asynchronous NATS spans are span-linked rather than sharing the trace ID, the view SHALL also make those linked spans discoverable rather than presenting the synchronous portion as the whole story.

#### Scenario: Operator inspects a completed demo trace
- **WHEN** an operator opens the provisioned dashboard and enters/selects the trace ID shown by the frontend after a demo run
- **THEN** the dashboard displays all spans sharing that trace ID with their names, originating component, and relative timing

#### Scenario: Operator reaches the span-linked NATS activity
- **WHEN** an operator viewing that trace looks for the asynchronous NATS processing spans
- **THEN** the dashboard surfaces them, or surfaces the span-link information needed to retrieve them, rather than silently omitting them

### Requirement: Dashboard and data source are provisioned automatically
The dashboarding tool's connection to the trace store and at least one dashboard capable of showing a trace SHALL be provisioned automatically as part of deploying the stack, without manual point-and-click setup.

#### Scenario: Fresh deployment has a working dashboard with no manual steps
- **WHEN** the observability stack is deployed for the first time
- **THEN** the dashboarding tool already has the trace store connected as a data source and at least one dashboard usable for viewing a trace, with no additional manual configuration required
