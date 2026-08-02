## Purpose

Defines the operational contract for standing up and tearing down the entire demo stack in a local `kind` (Kubernetes-in-Docker) cluster, so the trace-propagation demo is reproducible with a small, documented set of commands.

## ADDED Requirements

### Requirement: Single command creates the demo cluster
Bringing up the demo environment SHALL require running a documented, single cluster-creation command that provisions a `kind` cluster suitable for hosting all demo components (frontend, backend, NATS, relay proxy, collector, ClickHouse, Grafana).

#### Scenario: Fresh machine can create the cluster
- **WHEN** a developer with `kind` and `kubectl` installed runs the documented cluster-creation command
- **THEN** a running `kind` cluster exists and is ready to accept workload deployments

### Requirement: All components deploy with a documented, minimal command sequence
Deploying every demo component into the cluster SHALL require a small, documented sequence of commands (image build/load plus manifest apply), without requiring manual editing of manifests for a standard local run.

#### Scenario: Developer deploys the full stack
- **WHEN** a developer follows the documented deployment steps after the cluster exists
- **THEN** all seven workloads (frontend, backend, NATS, relay proxy, collector, ClickHouse, Grafana) reach a running/ready state in the cluster

### Requirement: Component startup respects dependency ordering
Components that depend on another component being available SHALL tolerate that dependency starting later (via retry/backoff or readiness gating), rather than crash-looping permanently when deployed concurrently.

#### Scenario: Backend starts before NATS is ready
- **WHEN** the backend workload starts before the NATS workload has become ready
- **THEN** the backend retries its NATS connection and successfully connects once NATS becomes ready, without being restarted by Kubernetes for repeated crashes

#### Scenario: Relay proxy starts before its config source exists
- **WHEN** the relay proxy starts before its Kubernetes configuration source has been created
- **THEN** the relay proxy waits/retries and begins serving evaluations once the configuration source becomes available, without crash-looping

### Requirement: Deployed services are reachable for demo interaction
After deployment, a developer SHALL be able to reach the frontend (or backend API, if the frontend is a static asset served separately) and the Grafana dashboard from their local machine using a documented access method.

#### Scenario: Developer accesses the frontend
- **WHEN** a developer follows the documented access instructions after deployment
- **THEN** they can load the frontend in a browser and trigger the demo flow

#### Scenario: Developer accesses Grafana
- **WHEN** a developer follows the documented access instructions after deployment
- **THEN** they can open the Grafana dashboard in a browser and view a completed trace

### Requirement: Cluster teardown is a single documented command
Tearing down the entire demo environment SHALL require a single documented command that removes the `kind` cluster and leaves no persistent local state requiring manual cleanup.

#### Scenario: Developer tears down after the demo
- **WHEN** a developer runs the documented teardown command
- **THEN** the `kind` cluster and all its workloads are removed
