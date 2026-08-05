## Purpose

Collects metrics from every component of the demo ecosystem into a single queryable store and surfaces them in dashboards, so the stack's behavior — especially under load — can be measured rather than guessed at.

## ADDED Requirements

### Requirement: Metrics from the whole ecosystem land in one store
A single metrics store SHALL hold metrics from every component of the demo stack, regardless of whether a component emits OTLP metrics or only exposes a Prometheus-style scrape endpoint.

#### Scenario: Application metrics are stored
- **WHEN** the backend has served demo requests
- **THEN** metrics emitted by the backend are queryable from the metrics store

#### Scenario: Infrastructure metrics are stored
- **WHEN** the stack has been running
- **THEN** metrics from the message broker, the trace store, the feature-flag relay proxy, and the dashboarding tool are queryable from the metrics store

#### Scenario: Collector self-telemetry is stored
- **WHEN** the collector has processed telemetry
- **THEN** the collector's own internal metrics are queryable from the metrics store, so pipeline health is observable rather than inferred

### Requirement: Collector accepts and routes metrics separately from traces
The collector SHALL accept OTLP metrics in addition to OTLP traces, and SHALL route each signal to its own destination: traces to the trace store, metrics to the metrics store.

#### Scenario: Metrics reach the metrics store
- **WHEN** a component exports OTLP metrics to the collector
- **THEN** those metrics are forwarded to the metrics store and become queryable there

#### Scenario: Trace behavior is unchanged
- **WHEN** a demo trace flow runs after metrics routing is configured
- **THEN** its spans still reach the trace store and remain queryable exactly as before, with no spans lost or diverted to the metrics store

### Requirement: Components expose their metrics endpoints
Components that ship an optional metrics endpoint SHALL have it enabled, so the metrics store has something to collect from.

#### Scenario: Message broker exposes metrics
- **WHEN** the message broker's metrics endpoint is queried
- **THEN** it returns broker metrics in a scrapeable format

#### Scenario: Relay proxy exposes metrics
- **WHEN** the feature-flag relay proxy's monitoring endpoint is queried
- **THEN** it returns relay-proxy metrics in a scrapeable format

### Requirement: Backend emits application metrics
The backend SHALL configure a metrics pipeline alongside its existing trace pipeline and export metrics over OTLP, so application-level behavior is measurable.

#### Scenario: Backend metrics appear after traffic
- **WHEN** the backend has handled demo requests
- **THEN** metrics attributable to the backend service are present in the metrics store

#### Scenario: Metrics export failure does not break request handling
- **WHEN** the metrics destination is unavailable
- **THEN** the backend continues serving demo requests successfully

### Requirement: Metrics are visualized without manual setup
The dashboarding tool SHALL have the metrics store provisioned as a data source and at least one dashboard for viewing ecosystem metrics, both configured automatically at deploy time.

#### Scenario: Fresh deployment has a working metrics dashboard
- **WHEN** the stack is deployed for the first time
- **THEN** the dashboarding tool already has the metrics store connected as a data source and a metrics dashboard available, with no manual configuration

#### Scenario: Operator inspects ecosystem metrics under load
- **WHEN** an operator opens the metrics dashboard while the stack is receiving traffic
- **THEN** the dashboard displays metrics from multiple components, including the collector's own pipeline health

### Requirement: Metrics storage is bounded for local use
The metrics store SHALL be configured with a bounded retention period and storage size appropriate for an ephemeral local cluster.

#### Scenario: Storage does not grow without limit
- **WHEN** the metrics store has been running and ingesting
- **THEN** its retention and volume size are explicitly configured rather than left at defaults intended for production
