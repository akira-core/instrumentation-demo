## Purpose

Drives synthetic OTLP telemetry at the demo pipeline on demand, so the stack can be put under sustained, adjustable load for performance testing instead of being exercised one browser click at a time.

## ADDED Requirements

### Requirement: Load generation is on demand, not always running
The load generator SHALL NOT run as part of the normal stack deployment. It SHALL be started explicitly by a developer and SHALL stop on its own once its configured run is complete.

#### Scenario: Normal deployment starts no load
- **WHEN** a developer deploys the stack the usual way
- **THEN** no load generator workload is running and the stack is idle until a demo request is made

#### Scenario: Developer starts a load test explicitly
- **WHEN** a developer runs the documented load-test command
- **THEN** a load generator starts sending telemetry to the pipeline

#### Scenario: Load test terminates on its own
- **WHEN** a load test is started with a bounded duration
- **THEN** the generator stops after that duration without further intervention, and does not restart

### Requirement: Load intensity and duration are configurable
The load generator's intensity and run length SHALL be adjustable at invocation time without editing manifests, so different performance scenarios can be exercised.

#### Scenario: Developer chooses a heavier load
- **WHEN** a developer starts a load test specifying a higher concurrency or batch size than the default
- **THEN** the generator runs with those settings

#### Scenario: Defaults are usable without any parameters
- **WHEN** a developer starts a load test without specifying any settings
- **THEN** it runs with documented defaults that are safe for a single-node local cluster

### Requirement: Generated load exercises the real pipeline
The load generator SHALL send telemetry to the same collector endpoint the demo's own services use, so the load traverses the real ingestion path rather than a test-only shortcut.

#### Scenario: Generated telemetry reaches the trace store
- **WHEN** a load test has run
- **THEN** the telemetry it generated is present in the trace store, having passed through the collector

#### Scenario: Load is visible in metrics
- **WHEN** a load test is running
- **THEN** its effect is observable in the metrics dashboard, including the collector's own throughput

### Requirement: Load test results are inspectable and cleanable
After a load test, its outcome SHALL be inspectable, and its resources SHALL be removable with a documented command so repeat runs do not conflict.

#### Scenario: Developer inspects a completed run
- **WHEN** a load test has finished
- **THEN** the developer can retrieve its logs or completion status

#### Scenario: Developer reruns a load test
- **WHEN** a developer runs the load-test command again after a previous run
- **THEN** it starts successfully rather than failing because of leftover resources from the previous run
