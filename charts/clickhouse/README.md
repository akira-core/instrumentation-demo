# clickhouse

All-in-one Helm chart for ClickHouse on Kubernetes:

- **Altinity ClickHouse Operator** (vendored subchart, alias `operator`)
- **ClickHouse Keeper** — `ClickHouseKeeperInstallation`, 3 nodes (smallest quorum)
- **ClickHouse** — `ClickHouseInstallation`, 1 shard × 2 replicas (smallest replicated HA)
- **Rotel** — OTLP collector writing traces/logs into ClickHouse, plus schema/TTL Jobs

The default profile is a *minimal HA* cluster sized to fit a single-node
Kubernetes (Docker Desktop, kind, minikube with ~4 CPU / 8Gi to spare). The
same topology scales to production by raising resources, storage, and
enabling anti-affinity — see [Scaling](#scaling).

```
                          ┌────────────────────────┐
   OTLP/gRPC :4317  ───►  │  rotel (Deployment)    │
   OTLP/HTTP :4318  ───►  │                        │
                          └───────────┬────────────┘
                                      │ HTTP :8123
                          ┌───────────▼────────────┐     ┌───────────────────┐
                          │  ClickHouse (CHI)      │◄───►│  Keeper (CHK)     │
                          │  1 shard × 2 replicas  │     │  3 replicas       │
                          └────────────────────────┘     └───────────────────┘
                                      ▲
                          ┌───────────┴────────────┐
                          │  Altinity Operator     │  reconciles CHI/CHK
                          └────────────────────────┘
```

## Prerequisites

- Kubernetes **>= 1.25** with a default StorageClass (or set
  `cluster.*.persistence.storageClassName`)
- `kubectl` and `helm` **>= 3.14** pointed at the target cluster
- Cluster-admin once, for the CRD install hook
- Roughly **2 CPU / 4Gi free** for the default profile
  (3 Keeper + 2 ClickHouse + operator + rotel requests)

## Step-by-step install

All commands run from the directory containing this chart
(`charts/clickhouse`). Substitute your own namespace and password throughout.

### 1. Pick a namespace

The operator defaults to namespace-scoped RBAC and watches its own
namespace, so the operator and the cluster live together:

```bash
export NS=clickhouse
kubectl create namespace "$NS"
```

### 2. Create the default user's password Secret

The chart never generates or stores a password — it references a Secret you
create. The `default` ClickHouse user (and rotel, which authenticates as it)
reads key `password` from Secret `clickhouse-default-user`:

```bash
kubectl create secret generic clickhouse-default-user \
  --from-literal=password='CHANGE_ME_STRONG' \
  -n "$NS"
```

Different Secret name or key? Set `cluster.clickhouse.defaultUser.existingSecret`
/ `existingSecretKey` at install time.

### 3. Install

Both subcharts (the Altinity operator and the local `cluster` chart) are
vendored as unpacked directories under `charts/`, so there is no
`helm dependency build` step and no network access needed — install straight
from the checkout:

```bash
helm upgrade --install clickhouse . -n "$NS" --timeout 15m
```

One release installs the CRDs (pre-install hook), the operator, the Keeper
quorum, the ClickHouse cluster, and rotel. On a fresh cluster the CRD hook
runs before anything else, so no two-step install is needed.

`--timeout 15m` matters on first install: the post-install schema Job
deliberately waits until every ClickHouse replica has joined the cluster
before running DDL (otherwise a replica that comes up late would miss the
`CREATE DATABASE ON CLUSTER` and stay empty), and image pulls plus PVC
provisioning can push the whole bring-up past helm's default 5-minute hook
wait.

Why one release works despite the internal dependencies — ordering is
layered, none of it manual:

1. **CRDs before CRs** — the operator subchart's `crdHook` is a
   `pre-install` hook Job; helm finishes it before applying any manifest,
   so the CHI/CHK custom resources always find their CRDs registered.
2. **Operator vs CRs** — declarative: if the CRs land before the operator
   is Ready, they simply wait in etcd until its reconcile loop picks them
   up.
3. **Keeper vs ClickHouse** — the operator wires the CHI to the CHK;
   ClickHouse pods restart until the Keeper quorum answers.
4. **Schema last** — the DDL and TTL Jobs are `post-install` hooks
   (weights 1 and 2) that poll until ClickHouse is up and every replica
   has joined the cluster before creating the otel database and tables.

### 4. Wait for the cluster to come up

```bash
# CRDs registered
kubectl wait --for=condition=Established \
  crd/clickhouseinstallations.clickhouse.altinity.com \
  crd/clickhousekeeperinstallations.clickhouse-keeper.altinity.com \
  --timeout=120s

# Watch the operator reconcile — STATUS becomes Completed when all hosts are up
kubectl get chk,chi -n "$NS" -w
```

First install pulls images and provisions PVCs; expect a few minutes. The
post-install DDL Job (`clickhouse-cluster-rotel-ddl`) retries until ClickHouse
answers, then creates the `otel` database and tables.

### 5. Verify

```bash
# All pods Running/Completed
kubectl get pods -n "$NS"

# Query through the client Service
kubectl run ch-client --rm -it --restart=Never -n "$NS" \
  --image=clickhouse/clickhouse-server:26.7.1.1315 -- \
  clickhouse-client --host clickhouse-cluster-clickhouse-client \
    --user default --password 'CHANGE_ME_STRONG' \
    --query "SELECT hostName(), version()"

# Replication healthy: both replicas listed for cluster 'default'
kubectl run ch-client --rm -it --restart=Never -n "$NS" \
  --image=clickhouse/clickhouse-server:26.7.1.1315 -- \
  clickhouse-client --host clickhouse-cluster-clickhouse-client \
    --user default --password 'CHANGE_ME_STRONG' \
    --query "SELECT cluster, host_name FROM system.clusters WHERE cluster = 'default'"
```

Send a test span and read it back:

```bash
kubectl port-forward -n "$NS" svc/clickhouse-cluster-rotel 4318:4318 &
curl -s http://localhost:4318/v1/traces \
  -H 'Content-Type: application/json' \
  -d '{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"smoke-test"}}]},"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174","name":"smoke","kind":1,"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000"}]}]}]}'
kill %1

kubectl run ch-client --rm -it --restart=Never -n "$NS" \
  --image=clickhouse/clickhouse-server:26.7.1.1315 -- \
  clickhouse-client --host clickhouse-cluster-clickhouse-client \
    --user default --password 'CHANGE_ME_STRONG' \
    --query "SELECT count() FROM otel.otel_traces"
```

### 6. Connect applications

In-cluster endpoints (release name `clickhouse`):

| Endpoint | Service |
|---|---|
| ClickHouse HTTP `:8123`, native `:9000` | `clickhouse-cluster-clickhouse-client.$NS.svc` |
| OTLP gRPC `:4317`, OTLP HTTP `:4318` | `clickhouse-cluster-rotel.$NS.svc` |

Pin friendlier names with `cluster.clickhouse.service.name` and
`cluster.rotel.name`.

## Uninstall

```bash
helm uninstall clickhouse -n "$NS"

# PVCs are retained on purpose (reclaimPolicy: Retain) — delete deliberately:
kubectl delete pvc -n "$NS" -l clickhouse.altinity.com/chi=clickhouse-cluster
kubectl delete pvc -n "$NS" -l clickhouse-keeper.altinity.com/chk=clickhouse-cluster

# CRDs are cluster-scoped and survive uninstall:
kubectl delete crd clickhouseinstallations.clickhouse.altinity.com \
  clickhouseinstallationtemplates.clickhouse.altinity.com \
  clickhousekeeperinstallations.clickhouse-keeper.altinity.com \
  clickhouseoperatorconfigurations.clickhouse.altinity.com
```

## Configuration

Common knobs (see `values.yaml` for the full commented list):

| Value | Default | Meaning |
|---|---|---|
| `cluster.clickhouse.replicas` | `2` | Replicas per shard (each holds the full dataset) |
| `cluster.clickhouse.shards` | `1` | Keep at 1 — see [Scaling](#scaling) |
| `cluster.clickhouse.persistence.size` | `20Gi` | Data volume per ClickHouse pod |
| `cluster.clickhouse.resources` | 0.5–1 CPU / 1–2Gi | Per ClickHouse pod |
| `cluster.clickhouse.defaultUser.existingSecret` | `clickhouse-default-user` | Pre-created Secret with the `default` user's password |
| `cluster.keeper.replicas` | `3` | Keeper quorum — do not change after first deploy |
| `cluster.keeper.persistence.size` | `5Gi` | Log volume per Keeper pod |
| `cluster.clickhouse.antiAffinity` | `false` | Set `true` + `podTemplate.nodeHostnameKey` on multi-node clusters |
| `cluster.rotel.enabled` | `true` | OTLP collector + schema Job |
| `cluster.rotel.exporter.ttl` | `168h` | Retention for otel tables (`0s` = keep forever) |
| `cluster.rotel.telemetry.{traces,logs,metrics}` | traces+logs | Signals rotel accepts and stores |
| `operator.rbac.namespaceScoped` | `true` | Role instead of ClusterRole; cluster must share the namespace |

### Extra users

`cluster.clickhouse.settings.extraUsersConfig` converts nested users/profiles
into Altinity configuration. Passwords come from Secrets via
`passwordSecret`:

```yaml
cluster:
  clickhouse:
    settings:
      extraUsersConfig:
        profiles:
          readonly:
            readonly: 1
            max_execution_time: 600
        users:
          reporter:
            passwordSecret:
              name: reporter-password   # kubectl create secret ... beforehand
              key: password
            profile: readonly
            networks:
              ip: "::/0"
            grants:
              query:
                - GRANT SELECT ON otel.otel_traces
```

### Why the otel database uses the Replicated engine

`cluster.rotel.exporter.databaseEngine` defaults to `Replicated`, which is
what makes adding a replica safe. Under an `Atomic` database a new replica
starts empty and the next `CREATE TABLE IF NOT EXISTS` gives it a fresh table
UUID — a second table under a different Keeper path the others never
replicate to. Writes split silently, with both sides reporting healthy. The
`Replicated` engine writes DDL to a Keeper log every member replays, so a new
replica inherits the schema with the original UUIDs and replicates
immediately.

Consequences:

- `cluster.rotel.exporter.cluster` must match `cluster.clickhouse.clusterName`.
- `cluster.rotel.exporter.engine` must be `ReplicatedMergeTree` (the chart
  rejects other combinations).
- A database engine cannot be changed in place. On an existing `Atomic`
  install the DDL Job warns and keeps what's there; converting means copying
  data out, dropping the database on every replica, and letting the Job
  recreate it.

### Retention (TTL)

`cluster.rotel.exporter.ttl` takes a number plus `s`/`m`/`h`/`d`; `0s` keeps
data forever. The DDL tool only writes TTL at table creation, so
`cluster.rotel.manageTtl` (default on) adds a post-upgrade Job that
re-applies the value with `ALTER TABLE … MODIFY TTL` and verifies it landed
on every replica. For different retention per signal, disable `manageTtl` and
run the ALTERs yourself.

### Operator RBAC scope

`operator.rbac.namespaceScoped: true` (default) gives the operator a
Role/RoleBinding in its own namespace instead of a ClusterRole. The
CHI/CHK must then live in the same namespace, and `operator.watchNamespaces`
may only name that namespace (the chart fails the render otherwise). For
cluster-wide operation set `namespaceScoped: false`.

## Scaling

Grow in this order — each step is a values change on the same topology:

1. **Resources** — raise `cluster.clickhouse.resources` and
   `persistence.size`; ClickHouse scales vertically very well.
2. **Spread out** — on a multi-node cluster set
   `cluster.{clickhouse,keeper}.antiAffinity: true` and
   `podTemplate.nodeHostnameKey: kubernetes.io/hostname` (plus
   `topologyZoneKey` for zone spread).
3. **Read replicas** — raise `cluster.clickhouse.replicas`. With the
   `Replicated` database engine the new replica syncs schema and data
   automatically.

**Sharding is not a values-only change.** Every replica holds the full
dataset, so a query against any node is complete. Raising
`cluster.clickhouse.shards` splits ingestion across shards, but the otel
tables have no `Distributed` table in front of them (the rotel DDL tool does
not create one) — queries would silently return one shard's worth of rows.
Shard only when ingest volume demands it, and put a `Distributed` table in
front of the otel tables first.

## Upgrading the vendored operator

```bash
helm repo add altinity https://helm.altinity.com
rm -rf charts/altinity-clickhouse-operator
helm pull altinity/altinity-clickhouse-operator \
  --version <new> --untar --untardir charts/
# bump dependencies[].version in Chart.yaml, then:
helm dependency update .
```

## References

- Altinity operator quick start: <https://github.com/Altinity/clickhouse-operator/blob/master/docs/quick_start.md>
- Altinity operator docs: <https://docs.altinity.com/altinitykubernetesoperator/>
- Rotel: <https://github.com/streamfold/rotel>
- ClickHouse Keeper: <https://clickhouse.com/docs/en/guides/sre/keeper/clickhouse-keeper>
