# clickhouse

Production-oriented **all-in-one Helm chart** for ClickHouse on Kubernetes.

| Subchart | Source | Role |
|----------|--------|------|
| **operator** | [Altinity clickhouse-operator](https://github.com/Altinity/clickhouse-operator/blob/master/docs/quick_start.md) Helm chart, vendored at `charts/altinity-clickhouse-operator/` | Altinity operator + CRDs (`ClickHouseInstallation` / `ClickHouseKeeperInstallation`) |
| **cluster** | Local (`charts/cluster`) | The CHI/CHK CRs, plus the [Rotel](https://github.com/rotel-dev/rotel) OTLP collector and its schema/TTL Jobs |

Default profile targets a **small-business production** footprint: HA without over-sharding.

## Architecture

```
                    ┌─────────────────────────────────────┐
                    │         clickhouse (umbrella)       │
                    │  values.yaml  (small-biz production)│
                    └──────────────┬──────────────────────┘
                                   │
           ┌───────────────────────┼───────────────────────┐
           ▼                                               ▼
 ┌─────────────────────┐                     ┌──────────────────────────┐
 │ operator (Altinity) │                     │ cluster (local subchart) │
 │ CRDs + controller   │  reconciles ──────► │ CHK Keeper (3)           │
 │ metrics exporter    │                     │ CHI ClickHouse (1×3)     │
 └─────────────────────┘                     └──────────────────────────┘
                                             │ rotel Deployment + Svc   │
                                             │ DDL Job / TTL Job        │
                                             └──────────────────────────┘
```

**Default topology**

| Component | Count | CPU (req–lim) | Memory (req–lim) | Disk / pod |
|-----------|-------|---------------|------------------|------------|
| ClickHouse Keeper | 3 | 500m–1 | 1–2 Gi | 20 Gi |
| ClickHouse server | 3 (1 shard) | 2–4 | 8–16 Gi | 200 Gi |
| Rotel collector | 1 | 50m–500m | 128–512 Mi | — |
| Operator manager | 1 | 50m–500m | 128–256 Mi | — |

Total rough floor: **~7.6 CPU / ~27 Gi RAM / ~660 Gi storage** (plus headroom for merges/queries).

**Images** are pinned explicitly in `values.yaml` as `registry` / `repository` / `tag`,
so a mirror is a one-key override and no tag floats:

| Image | Default |
|-------|---------|
| ClickHouse server / Keeper | `docker.io/clickhouse/clickhouse-{server,keeper}:26.7.1.1315` |
| Operator | `altinity/clickhouse-operator:0.27.2` + `altinity/metrics-exporter:0.27.2` |
| Rotel + DDL tool | `docker.io/streamfold/rotel{,-clickhouse-ddl}:v0.2.2` |

## Prerequisites

1. **Kubernetes** ≥ 1.25 (Altinity operator 0.16+)
2. **Helm** ≥ 3.8
3. A **StorageClass** suitable for databases (SSD, expandable). Set:

```yaml
cluster:
  keeper:
    persistence:
      storageClassName: gp3   # EKS example
  clickhouse:
    persistence:
      storageClassName: gp3
```

## Install

### Local kind / single-node

```bash
make deps
make install VALUES=values-local.yaml PASSWORD='localdev'
kubectl -n clickhouse get chi,chk,pods
kubectl -n clickhouse exec deploy/chi-ch-aio-cluster-default-0-0 -- \
  clickhouse-client --password localdev -q 'SELECT version()'
# or the StatefulSet pod:
kubectl -n clickhouse exec chi-ch-aio-cluster-default-0-0-0 -- \
  clickhouse-client --password localdev -q 'SELECT version()'
```

`values-local.yaml` drops anti-affinity, uses 1 Keeper + 1 ClickHouse replica, smaller disks, and skips the rotel TTL hook (single-node `clusterAllReplicas` auth).

### Production

```bash
# From this repo root
helm dependency update   # or: make deps
helm lint .

# Production (small business defaults)
helm upgrade --install ch-aio . \
  --namespace clickhouse \
  --create-namespace \
  --set cluster.clickhouse.defaultUser.password='CHANGE_ME_STRONG'

# Recommended first install (avoids CRD readiness race):
#   make install-operator VALUES=values.yaml
#   make install-cluster  VALUES=values.yaml PASSWORD='CHANGE_ME_STRONG'

# Or use an existing Secret
kubectl -n clickhouse create secret generic ch-default-password \
  --from-literal=password='CHANGE_ME_STRONG'
helm upgrade --install ch-aio . -n clickhouse \
  --set cluster.clickhouse.defaultUser.existingSecret=ch-default-password \
  --set cluster.clickhouse.defaultUser.autoGenerate=false
```

### Dev / local cluster

The defaults size for production. On kind/k3d/minikube, shrink the request, drop
to a single replica, and clear the two placement keys — a one-node cluster has
no zone labels and cannot give each pod its own node:

```bash
helm upgrade --install ch-aio . -n clickhouse --create-namespace \
  --set cluster.clickhouse.replicas=1 \
  --set cluster.keeper.replicas=1 \
  --set cluster.clickhouse.resources.requests.cpu=500m \
  --set cluster.clickhouse.resources.requests.memory=1Gi \
  --set cluster.clickhouse.persistence.size=20Gi \
  --set cluster.keeper.persistence.size=5Gi \
  --set cluster.clickhouse.podTemplate.topologyZoneKey="" \
  --set cluster.clickhouse.podTemplate.nodeHostnameKey="" \
  --set cluster.keeper.podTemplate.topologyZoneKey="" \
  --set cluster.keeper.podTemplate.nodeHostnameKey="" \
  --set cluster.clickhouse.defaultUser.password='devpass'
```

With `replicas=1` the spread constraint is harmless, but `nodeHostnameKey` still
has to go the moment you raise it above the node count.

`keeper.replicas` cannot be changed after the first successful deploy, so pick
1 (local) or 3 (production) up front.

### Optional TLS

Mutual TLS for ClickHouse ↔ Keeper and client connections. Provide a
cert-manager `Issuer` / `ClusterIssuer`, then:

```bash
helm upgrade --install ch-aio . -n clickhouse \
  --set cluster.tls.enabled=true \
  --set cluster.tls.issuerRef.name=local-issuer \
  --set cluster.tls.issuerRef.kind=Issuer
```

## Verify

```bash
kubectl get pods,chi,chk -n clickhouse
kubectl get chi,chk -n clickhouse -o wide

# Client
kubectl exec -it -n clickhouse <clickhouse-pod> -- clickhouse-client
```

## Configuration highlights

| Key | Default | Notes |
|-----|---------|--------|
| `operator.enabled` | `true` | Set `false` if operator is already cluster-wide |
| `cluster.keeper.replicas` | `3` | **Odd only; do not change after first deploy** |
| `cluster.clickhouse.replicas` | `3` | Each replica holds the full dataset |
| `cluster.clickhouse.shards` | `1` | **Leave at 1** — see "Scaling path" |
| `cluster.clickhouse.persistence.size` | `200Gi` | Per replica |
| `cluster.clickhouse.persistence.perReplica` | `[]` | Per-replica StorageClass / size overrides |
| `cluster.clickhouse.resources` | 2–4 CPU / 8–16Gi | Tune to node size |
| `cluster.tls.enabled` | `false` | Needs cert-manager + an Issuer |
| `cluster.rotel.enabled` | `true` | OTLP collector (traces/logs → ClickHouse) |
| `cluster.rotel.telemetry.{traces,logs,metrics}` | `true/true/false` | Off also closes that OTLP receiver |
| `cluster.rotel.exporter.tablePrefix` | `otel` | Tables are `<prefix>_traces` / `<prefix>_logs` |
| `cluster.rotel.exporter.{traces,logs}.tablePrefix` | `""` | Per-signal override; empty inherits the above |
| `cluster.rotel.autoscaling.enabled` | `false` | HPA on the collector; needs metrics-server |
| `cluster.rotel.logFormat` | `json` | Agent stdout: `text` or `json` |
| `cluster.rotel.internalMetrics.enabled` | `false` | Rotel runtime metrics → VictoriaMetrics |
| `cluster.rotel.internalMetrics.endpoint` | `""` | VM OTLP base URL (Rotel appends `/v1/metrics`) |
| `cluster.rotel.exporter.engine` | `ReplicatedMergeTree` | `MergeTree` for single replica |
| `cluster.rotel.exporter.databaseEngine` | `Replicated` | Keeps a new replica's table UUIDs aligned |
| `cluster.rotel.exporter.ttl` | `168h` | Retention; `<n><s\|m\|h\|d>`, `0s` = forever |
| `cluster.rotel.manageTtl` | `true` | Re-apply `ttl` to existing tables on upgrade |
| `operator.rbac.namespaceScoped` | `true` | Role instead of ClusterRole |
| `operator.watchNamespaces` | `[clickhouse]` | Must equal the release namespace |
| `cluster.clickhouse.settings.extraUsersConfig` | `reporter` | Users, profiles, row filters |
| `cluster.*.podTemplate.topologyZoneKey` | `topology.kubernetes.io/zone` | Domain replicas spread across |
| `cluster.*.podTemplate.nodeHostnameKey` | `kubernetes.io/hostname` | One pod per node; excess stay `Pending` |
| `cluster.*.podTemplate.topologySpreadConstraints` | `[]` | Operator field; merges by `topologyKey` |

Full knobs: `values.yaml` and `charts/cluster/values.yaml`.

### Why the otel database is Replicated

`cluster.rotel.exporter.databaseEngine` defaults to `Replicated`, which is what
makes adding a replica safe.

The table path is `/clickhouse/tables/{uuid}/{shard}`, so two servers are
replicas of one table only when they agree on its UUID. `ON CLUSTER` gives them
a shared UUID *when they all run the same CREATE* — it does not backfill the
original UUID later. Under an `Atomic` database a brand-new replica therefore
starts empty, and the next `CREATE TABLE IF NOT EXISTS` run gives it a fresh
UUID: a second table under a different Keeper path that the others never
replicate to. Writes split silently, with both tables reporting healthy.

The `Replicated` engine writes each DDL statement to a Keeper log that every
member replays, so a new replica inherits the schema *with the original UUIDs*
and starts replicating immediately. It is also the only thing the operator's
`enableDatabaseSync` supports, and the same engine the operator gives its own
`default` database.

Consequences worth knowing:

- `rotel.exporter.cluster` must be set. The `CREATE DATABASE` still runs
  `ON CLUSTER` so every host joins; only the statements after it are replicated.
- The DDL tool runs without `--cluster` and the TTL job without `ON CLUSTER`.
  Both would otherwise hand each host a statement the database engine is
  already going to deliver.
- `engine` must be `ReplicatedMergeTree`. Replicating DDL to hosts that each
  keep their own copy of the data is not replication, so the chart rejects the
  combination.

Check it landed with the CR's own condition:

```bash
kubectl get chi <name> -o wide   # STATUS=Completed when hosts are ready
# ReplicasInSync / "All replicas are in sync"
```

**Migrating an existing install.** A database engine cannot be changed in place,
and `CREATE DATABASE IF NOT EXISTS` keeps whatever is already there — so setting
this on a running cluster does nothing on its own. The DDL job compares the two
and warns in its log rather than failing the upgrade. To convert, copy the data
out, drop the database on **every** replica, let the job recreate it, and copy
back:

```sql
CREATE DATABASE otel_old ENGINE = Atomic;         -- on one replica
CREATE TABLE otel_old.otel_traces AS otel.otel_traces;
INSERT INTO otel_old.otel_traces SELECT * FROM otel.otel_traces;
-- repeat per table, DROP DATABASE otel SYNC on every replica, helm upgrade,
-- then INSERT INTO otel.otel_traces SELECT * FROM otel_old.otel_traces
```

If a replica already holds an `Atomic` database of that name — a reused volume
from an earlier scale-out, say — the sync cannot proceed and the operator
reports `SchemaInSync: False` with `DatabasesNotCreated`. Dropping the stale
database on that replica lets the operator recreate it with the right engine.

### Retention (TTL)

Set `cluster.rotel.exporter.ttl` — a number plus `s`, `m`, `h` or `d`, with
`0s` meaning keep forever:

```bash
helm upgrade ch-aio . -n clickhouse --set cluster.rotel.exporter.ttl=30d
```

The DDL tool only writes TTL when it **creates** a table, so on an existing
install that value alone changes nothing. `cluster.rotel.manageTtl` (default on)
adds a post-upgrade Job that re-applies it with `ALTER TABLE ... MODIFY TTL`,
which is what makes the value adjustable after the first install.

The job reuses each table's own TTL expression — `Timestamp` for spans,
`TimestampTime` for logs, `Start` for the trace-id index — so it stays correct
if the DDL tool's schema changes, and falls back to those columns by table
suffix when a table currently has no TTL (otherwise `0s` would be a one-way
door). It then reads the result back from every replica through
`clusterAllReplicas` and exits non-zero on a mismatch, so a replica that was
restarting during the upgrade is picked up by the Job's retry rather than
silently left on the old retention.

The otel tables carry `ttl_only_drop_parts = 1`: whole parts are dropped once
every row in them has expired, instead of rewriting parts to delete rows.
Retention is therefore granular to the partition, which is one day.

To retain traces and logs for different periods, turn `manageTtl` off and run
the `ALTER TABLE ... MODIFY TTL` statements yourself — the chart drives a single
value for every table.

### Operator RBAC scope

`operator.rbac.namespaceScoped: true` gives the operator a Role/RoleBinding in its
own namespace instead of a ClusterRole, so it can only touch StatefulSets,
Secrets, PVCs and custom resources there. Two consequences:

- **The cluster must live in the operator's namespace.** Both `make
  install-operator` and `make install-cluster` use `NAMESPACE` for exactly this
  reason.
- **`controller.watchNamespaces` must list that namespace.** An empty list means
  cluster-wide, which a namespaced Role cannot serve — the operator reconciles
  nothing and only logs permission errors. The chart fails to render on that
  mismatch rather than letting it reach the cluster.

Two ClusterRoles remain when `metrics.secure` is on; they cover only the
`TokenReview`/`SubjectAccessReview` calls that authenticate metrics scrapes.

To manage clusters across several namespaces, set `operator.rbac.namespaceScoped:
false` and either list them in `watchNamespaces` or leave it empty for
cluster-wide.

### ClickHouse users

There is one mechanism: `cluster.clickhouse.settings.extraUsersConfig`, passed
through to the operator verbatim. A user is a profile, a set of grants, and
optionally a per-table row filter.

| | Sees |
|---|---|
| grant, no filter | the **whole** table |
| grant + filter | only the rows matching the filter |
| no grant | nothing — `ACCESS_DENIED`, whatever the filters say |

Grants are table- and column-level; they cannot express "these rows". Row
filtering is a separate concept and lives under `databases.<db>.<table>.filter`.

`values.yaml` carries a `reporter` user reading the otel tables in full, and a
commented `team_a` showing the same grants narrowed to one Kubernetes namespace.
Both hang off a shared `readonly_user` profile with `readonly: 1` plus memory,
runtime and result-size ceilings.

If the list outgrows `values.yaml`, split it into a second values file and pass
both with `-f`. That is the only place the split can happen — the operator
cannot source users from a ConfigMap or Secret, since its `externalSecret` field
carries cluster secrets only.

**Grants and filters are yours to keep in step.** A granted table with no filter
entry returns **every** row — that is the one mistake worth re-reading the file
for. `otel_traces_trace_id_ts` is never granted: it holds only trace ids and
timestamps, so there is nothing to filter on.

**Row policies do not leak between users.** They are created with
`apply_to_all = 0` and bind only the users they name, so a whole-table reader is
unaffected by another user's filter. Verify with:

```sql
SELECT name, apply_to_all, apply_to_list FROM system.row_policies
```

**The namespace has to be on the telemetry.** Rotel does not enrich spans with
Kubernetes metadata, so instrumented workloads must publish it themselves:

```yaml
env:
  - name: POD_NAMESPACE
    valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
  - name: OTEL_RESOURCE_ATTRIBUTES
    value: k8s.namespace.name=$(POD_NAMESPACE)
```

Rows missing the attribute match no filter and stay invisible to every scoped
user.

**Passwords come from Secrets you create.** The chart generates none for these
users; each password is injected as container env and read back with
`@from_env`, so it reaches neither the CR nor
`preprocessed_configs/users.xml`. The Secret must exist **before** the upgrade —
a missing one leaves every ClickHouse pod in `CreateContainerConfigError`, not
just that user disabled. Rotating a password needs a pod restart, since env is
fixed at container start.

**Config-defined users are read-only at runtime.** Granting another table later
means editing the file and running `helm upgrade`; a `GRANT` statement against a
config-sourced user is rejected. Check what is actually loaded, and from where,
with:

```sql
SHOW CREATE USER reporter;
SELECT name, storage FROM system.settings_profiles;
SELECT user_name, inherit_profile FROM system.settings_profile_elements
WHERE user_name IS NOT NULL;
```

`storage = users_xml` marks the ones this chart manages; anything else was
created by hand with SQL and will not survive a cluster rebuild.

### A different StorageClass per replica

The CRD carries one `dataVolumeClaimSpec` for the whole cluster, so the operator
cannot vary storage per replica. It does create **one StatefulSet per replica**
with a deterministic claim name, though, and a StatefulSet adopts a claim that
already carries that name without comparing its StorageClass against the
template. `cluster.clickhouse.persistence.perReplica` pre-creates those claims:

```yaml
cluster:
  clickhouse:
    persistence:
      storageClassName: standard-ssd    # every replica not listed below
      size: 200Gi
      perReplica:
        - replica: 0
          storageClassName: fast-nvme
        - replica: 1
          storageClassName: standard-ssd
          size: 500Gi
```

Rendered name: `clickhouse-storage-volume-<clickhouseName>-clickhouse-<shard>-<replica>-0`.
The chart rejects a replica or shard index outside the configured counts and a
pair claimed twice, so a typo cannot silently leave the StatefulSet to create
its own claim from the template.

**The cluster name has to stay short.** The operator caps a StatefulSet name at
63 characters and, past that, shortens the middle and splices in a hash — a
50-character cluster name produces `<name>-cli-7215-0-0`, not
`<name>-clickhouse-0-0`. The predicted claim would then belong to no
StatefulSet, and the real one would quietly build its own on the default
StorageClass. The chart refuses to render in that case; keep the name
(`<release>-cluster`, or `cluster.fullnameOverride` / `clickhouse.name`) at
**48 characters or fewer** for a single-digit shard and replica index. The
operator itself gives up entirely somewhere past ~52 characters, where the
`<name>-clickhouse` label it applies exceeds 63 bytes and reconcile fails.

Ordering is the whole trick — the claim has to exist before the operator creates
the StatefulSet. The PVCs sync in wave 2, one ahead of the `CHI`,
and a plain `helm install` gets it from Helm's own kind ordering, which puts
`PersistentVolumeClaim` ahead of custom resources.

Two things to know before using it. The claims carry
`helm.sh/resource-policy: keep`, so `helm uninstall` leaves them behind — unlike
operator-created claims they would otherwise be deleted with the release, taking
the data. And a `size` change here never reaches a claim that already exists;
expand it with `kubectl patch pvc` instead.

Worth doing only when the replicas are **deliberately** asymmetric — a cold
replica kept for backups, say. ClickHouse replication assumes comparable
hardware, and rotel writes through the headless Service, so a slower replica
both lags on merges and still serves its share of queries.

### Pod scheduling and naming

Every pod the chart is responsible for takes `nodeSelector`, `tolerations` and
`affinity`. `values.yaml` carries a commented example for each.

| Pod | Scheduling | Labels / annotations |
|-----|------------|----------------------|
| Keeper | `cluster.keeper.podTemplate.*` | `cluster.keeper.{labels,annotations}` |
| ClickHouse server | `cluster.clickhouse.podTemplate.*` | `cluster.clickhouse.{labels,annotations}` |
| Version-probe Job | `cluster.clickhouse.versionProbe.nodeSelector` — **`nodeSelector` only**, the CRD has no tolerations or affinity field | `cluster.clickhouse.versionProbe.{labels,annotations}` |
| Rotel collector | `cluster.rotel.{nodeSelector,tolerations,affinity}` | `cluster.rotel.{podLabels,podAnnotations}` |
| Schema + TTL Jobs | `cluster.rotel.jobs.{nodeSelector,tolerations,affinity}` | `cluster.rotel.jobs.{podLabels,podAnnotations}` |
| Operator manager | `operator.manager.{nodeSelector,tolerations,affinity}` | — |

The split in that table is not cosmetic. Keeper and ClickHouse pods are created
by the operator, not by the chart, and **neither CRD's `podTemplate` has a
`labels` or `annotations` field**. A structural CRD schema prunes unknown fields
without raising an error, so metadata set there is dropped between `kubectl` and
etcd — visible nowhere, and easy to mistake for the operator ignoring it. The
operator's actual hook is `spec.labels` / `spec.annotations` on the CR, which it
merges into everything it creates for that cluster: StatefulSets, Pods, the
headless Service, ConfigMaps, Secrets, PodDisruptionBudgets. That is what
`cluster.{keeper,clickhouse}.{labels,annotations}` set. Setting them under
`podTemplate` fails the render with a pointer to the right key.

`cluster.commonLabels` feeds the same `spec.labels`, so a label set once at the
chart level now reaches the operator-managed resources too, not just the
chart-managed ones. Per-component `labels` are merged on top and win on a key
collision; operator-owned labels (`clickhouse.altinity.com/*`) win over both.

Two cautions. Both fields land in the StatefulSet pod template, so changing them
rolls the pods — they do **not** enter `spec.selector`, so a live cluster does
accept the change. And the operator also stamps annotations onto the
StatefulSet's `volumeClaimTemplates`, which Kubernetes treats as immutable.

Two scheduling traps worth naming. A `nodeSelector` alone will not place a pod on
a **tainted** node — pair it with tolerations. And the schema/TTL Jobs are helm
hooks, so one that can never schedule blocks the whole `helm upgrade` until it
times out; give them the same tolerations as the database nodes they talk to.

Names default to `<release>-cluster` — the two CRs, the rotel
Deployment/Service/Jobs, the generated password Secret and the per-replica PVCs
all derive from it.

| Key | Renames | Safe to change later? |
|-----|---------|-----------------------|
| `cluster.fullnameOverride` | all of the above, together | Yes on paper, but the operator reads the new CR as a different cluster |
| `cluster.nameOverride` | the same names, **plus `app.kubernetes.io/name`** | **No — install-time only** |
| `cluster.clickhouse.name` | the ClickHouseInstallation (CHI) CR and the per-replica PVCs | No |
| `cluster.keeper.name` | the ClickHouseKeeperInstallation (CHK) CR | No |
| `cluster.rotel.name` | the collector Deployment/Service/Jobs | Only by repointing every SDK |

`nameOverride` is the one to be careful with: `app.kubernetes.io/name` is a
selector label, and a Deployment's `spec.selector` is immutable, so changing it
on a live release fails the upgrade until the rotel Deployment is deleted by
hand. `fullnameOverride` leaves the labels alone.

`clickhouse.name` and `keeper.name` are independent — setting one and not the
other splits a pair the official examples keep matching, and the collector's
exporter endpoint follows the ClickHouse one.

### Replica placement

The operator derives scheduling rules from two keys rather than taking a raw pod
spec. Both are at the values its
[CHI/CHK custom resource docs](https://github.com/Altinity/clickhouse-operator/blob/master/docs/custom_resource_explained.md)
recommends:

| Key | Default | Operator emits |
|-----|---------|----------------|
| `topologyZoneKey` | `topology.kubernetes.io/zone` | **required** TopologySpreadConstraint (`maxSkew: 1`, `DoNotSchedule`) + **preferred** PodAntiAffinity |
| `nodeHostnameKey` | `kubernetes.io/hostname` | **required** PodAntiAffinity across every pod of the cluster — one per node, regardless of shard |

They answer different questions. `topologyZoneKey` is *balance* — spread the
replicas of one shard evenly over failure domains. `nodeHostnameKey` is
*exclusion* — never put two pods of this cluster on one machine, whatever the
skew says. Setting both is the whole of the official HA recipe; the docs call it
"pods across availability zones **without manual affinity rules**".

Fewer zones than replicas is **not** a problem: `maxSkew: 1` allows several
replicas per domain, still evenly balanced. What does strand pods is a node
carrying no zone label at all (a `DoNotSchedule` constraint skips it) or
`nodeHostnameKey` on a cluster with fewer nodes than replicas.

**Which of the two you can loosen afterwards is not symmetric.** The API
reference words the two escape hatches differently, and the difference is load
bearing:

| Field | Interaction with the operator's own rules |
|-------|-------------------------------------------|
| `topologySpreadConstraints` | *merged by `topologyKey`* — repeat `topologyZoneKey`'s value and your entry replaces the generated one |
| `affinity` | *appended; scheduling term lists are concatenated* — nothing can be subtracted |

So the zone rule is adjustable and the node rule is not. To relax the spread,
write the operator's field out in full:

```yaml
cluster:
  clickhouse:
    podTemplate:
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: ScheduleAnyway
```

Omit `labelSelector` on an entry targeting `topologyZoneKey`: the operator fills
in the pod labels, including the shard id, and a constraint with an empty
selector matches nothing.

`nodeHostnameKey` has no equivalent — its rule is a PodAntiAffinity, so clear
the key itself or live with it. Single-node clusters need both keys cleared, as
in "Dev / local cluster" above.

The chart deliberately offers **no shorthand** for loosening the spread. Doing
so is a step away from the posture the operator's docs recommend, so it is
spelled out as the operator's own field rather than hidden behind a
chart-invented value.

Spread across nodes rather than AZs — one value, useful on a cluster with no
zone labels:

```yaml
cluster:
  clickhouse:
    podTemplate:
      topologyZoneKey: kubernetes.io/hostname
```

Drop exclusive node occupancy, keeping the zone spread (replicas may then share
a node):

```yaml
cluster:
  clickhouse:
    podTemplate:
      nodeHostnameKey: ""
```

Switching an existing cluster from strict to best-effort can deadlock: the
operator updates replicas one at a time and waits for each to become ready,
while the not-yet-updated replicas still carry the required PodAntiAffinity that
blocks the new pod. Apply the change before scaling up, or drop the stale rule
from the remaining StatefulSets to let the rollout finish.

### Deploying with ArgoCD

One Application installs operator + cluster in order. There is nothing to turn
on: every chart-owned resource ships an `argocd.argoproj.io/sync-wave`, which a
plain `helm install` ignores. Operator resources are un-annotated and therefore
wave 0, and the cluster follows in dependency order:

| Wave | Resources |
|------|-----------|
| 0 | operator (Deployment, CRDs, RBAC, webhooks) |
| 1 | cert-manager `Certificate`s, when `tls.createCertificates` |
| 2 | `CHK`, pre-created per-replica PVCs |
| 3 | `CHI`, the ClusterIP client Service |
| 4 | Rotel Deployment / Service / HPA |

The CRs also carry `SkipDryRunOnMissingResource=true`, so the first sync does not
fail dry-run against CRDs the operator has not registered yet. The rotel DDL and
TTL Jobs carry Helm `post-install/post-upgrade` hooks, which ArgoCD runs as a
PostSync hook — after every wave.

**Keeper is a wave ahead of ClickHouse on purpose.** The operator does not gate
the ClickHouse rollout on Keeper. Its `reconcileClusterRevisions` step blocks
only while the `CHK` *object* is missing; once the object exists, a
`Ready` condition that is still false is logged and passed over
([`internal/controller/clickhouse/sync.go`](https://github.com/ClickHouse/clickhouse-operator)).
The keeper endpoint list is built from `spec.replicas` rather than from running
pods, so the ClickHouse config renders and the StatefulSets roll while Keeper has
no quorum. Nothing corrupts — the server retries the connection — but `ON CLUSTER`
DDL fails and replicated tables stay read-only until quorum forms. The wave split
is what actually orders the two, which makes the health checks below load-bearing
rather than cosmetic.

```yaml
apiVersion: argoproj.io/v1beta1
kind: Application
metadata:
  name: clickhouse
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/Marz32onE/clickhouse-aio
    targetRevision: main
    path: .
    helm:
      values: |
        cluster:
          clickhouse:
            defaultUser:
              existingSecret: ch-default-password   # create it out-of-band
              autoGenerate: false
  destination:
    server: https://kubernetes.default.svc
    namespace: clickhouse
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true    # operator CRDs exceed client-side apply limits
    retry:
      limit: 5
      backoff: {duration: 20s, factor: 2, maxDuration: 3m}
```

**Register the CR health checks — the waves are inert without them.** ArgoCD
calls an unknown custom resource Healthy the moment it is created, so wave 3
starts while Keeper is still electing a leader and the whole split collapses back
to apply-ordering. Add to `argocd-cm`:

```yaml
resource.customizations.health.clickhouse-keeper.altinity.com_ClickHouseKeeperInstallation: |
  hs = {}
  if obj.status ~= nil and obj.status.conditions ~= nil then
    for _, c in ipairs(obj.status.conditions) do
      if c.type == "Ready" and c.status == "True" then
        hs.status = "Healthy"; hs.message = c.message; return hs
      end
    end
  end
  hs.status = "Progressing"; hs.message = "waiting for quorum"
  return hs
resource.customizations.health.clickhouse.altinity.com_ClickHouseInstallation: |
  hs = {}
  if obj.status ~= nil and obj.status.conditions ~= nil then
    for _, c in ipairs(obj.status.conditions) do
      if c.type == "Ready" and c.status == "True" then
        hs.status = "Healthy"; hs.message = c.message; return hs
      end
    end
  end
  hs.status = "Progressing"; hs.message = "waiting for replicas"
  return hs
```

Both gate on `Ready`, not `Healthy`. On a `CHK` the operator sets
`Ready` from quorum — one leader plus `ceil(n/2) - 1` followers — while `Healthy`
means *every* replica is serving. Quorum is what ClickHouse needs, and gating on
`Healthy` would stall wave 3 on a single unavailable Keeper pod that the cluster
tolerates fine.

**The password has to come from an existing Secret** (or a fixed value). ArgoCD
renders manifests without cluster access, so the `lookup` behind
`defaultUser.autoGenerate` finds nothing and mints a new password on every sync.
The chart cannot detect this — a renderer gives no way to tell "no cluster" from
"first install" — so it does not try to fail fast; setting
`defaultUser.existingSecret` is on you. If `autoGenerate` has to stay, stop
ArgoCD from reconciling the value it re-renders:

```yaml
  ignoreDifferences:
    - group: ""
      kind: Secret
      name: clickhouse-cluster-default-password   # <release>-cluster-default-password
      jsonPointers:
        - /data
```

### Offline / air-gapped install

The operator chart is **vendored unpacked** at `charts/clickhouse-operator-helm/`
and referenced via `file://`, so `helm dependency update`, `lint`, and `install`
need no registry access — clone and install.

Still required in the air-gapped environment: the **container images**
(mirror to your private registry and override the repositories):

```
docker.io/clickhouse/clickhouse-server:26.7.1.1315
docker.io/clickhouse/clickhouse-keeper:26.7.1.1315
ghcr.io/clickhouse/clickhouse-operator:v0.0.7
docker.io/streamfold/rotel:v0.2.2
docker.io/streamfold/rotel-clickhouse-ddl:v0.2.2
quay.io/jetstack/cert-manager-*:v1.21.0
```

Every one of those is a `registry` / `repository` / `tag` triple in
`values.yaml`, so pointing at a mirror is `--set ...image.registry=my.registry`
rather than a rewrite of each repository string.

To refresh the vendored operator chart when a new release ships:

```bash
rm -rf charts/clickhouse-operator-helm
helm pull oci://ghcr.io/clickhouse/clickhouse-operator-helm \
  --version <new-version> --untar --untardir charts/
# bump dependencies[].version in Chart.yaml, then:
helm dependency update
```

### Operator-only install

Same namespace as the cluster — `operator.rbac.namespaceScoped` scopes the operator's
Role to its own namespace. See "Operator RBAC scope" above.

```bash
helm upgrade --install ch-operator . -n clickhouse --create-namespace \
  --set cluster.enabled=false
```

### Cluster-only (operator already installed)

```bash
helm upgrade --install ch-cluster . -n clickhouse --create-namespace \
  --set operator.enabled=false
```

## OTLP ingestion (Rotel) + ClickStack UI

The chart deploys [Rotel](https://github.com/rotel-dev/rotel), a lightweight Rust
OTLP collector, writing traces and logs into ClickHouse with the standard
OpenTelemetry ClickHouse-exporter schema (`otel.otel_traces`, `otel.otel_logs`).
A post-install Job creates the schema via `rotel-clickhouse-ddl`
(`ReplicatedMergeTree` + `ON CLUSTER default` by default — matches the
operator's cluster/macros config).

Point your apps / SDKs at:

```
OTLP/gRPC  <cluster-name>-rotel.<namespace>.svc:4317
OTLP/HTTP  <cluster-name>-rotel.<namespace>.svc:4318
```

`cluster.rotel.telemetry.{traces,logs,metrics}` selects the signals. A signal
turned off gets no tables from the DDL Job, no exporter in the deployment, and
its OTLP receiver closed — the three are generated from one list and cannot
drift apart. Agent stdout (`logFormat`) is pod logs only and is never written
to ClickHouse.

### Table names

Rotel builds table names as `<prefix>_<signal>`; only the prefix is
configurable. `exporter.tablePrefix` sets the default, and each signal can
override it:

```yaml
cluster:
  rotel:
    exporter:
      tablePrefix: otel
      traces: {tablePrefix: app}   # otel.app_traces
      logs:   {tablePrefix: sys}   # otel.sys_logs
```

Signals sharing a prefix share one exporter and one connection pool; differing
prefixes get one exporter each. The database is shared. Changing a prefix on a
live install creates a new empty table — the old one keeps its rows and ages
out under its own TTL.

### Autoscaling the collector

```bash
helm upgrade ch-aio . -n clickhouse \
  --set cluster.rotel.autoscaling.enabled=true \
  --set cluster.rotel.autoscaling.maxReplicas=6
```

Renders an `autoscaling/v2` HPA on CPU (75% of `resources.requests.cpu` by
default) and stops rendering `replicas` on the Deployment, so a helm upgrade no
longer resets what the HPA chose. Needs metrics-server. Scaling out multiplies
in-flight ClickHouse inserts: with `async_insert` on, more replicas means more
smaller batches, so raise batch sizes before raising `maxReplicas`.

### Rotel's own runtime metrics

```bash
helm upgrade ch-aio . -n clickhouse \
  --set cluster.rotel.internalMetrics.enabled=true \
  --set cluster.rotel.internalMetrics.endpoint=http://vmsingle.monitoring.svc:8428/opentelemetry
```

Adds an OTLP/HTTP exporter to VictoriaMetrics alongside the ClickHouse ones and
routes Rotel's internal metrics to it (the base URL gets `/v1/metrics`
appended). ClickHouse keeps whatever signals `telemetry` enables.

Rotel only starts its internal-metrics pipeline when the regular metrics
pipeline is active, so enabling this also holds the OTLP metrics receiver open.
The consequence depends on `telemetry.metrics`:

| `telemetry.metrics` | App metrics sent to Rotel | Rotel's own metrics |
|---------------------|---------------------------|---------------------|
| `false` (default) | VictoriaMetrics | VictoriaMetrics |
| `true` | ClickHouse `<prefix>_metrics_*` | VictoriaMetrics |

Notes:
- Port `9363` is reserved by the operator for Prometheus metrics — the
  validation webhook rejects it in `additionalPorts`, and no `prometheus`
  entry is needed in `extraConfig`.
- The operator's version-probe Job defaults to 256Mi and OOMs with
  ClickHouse ≥ 26.x images; the chart bumps it via
  `cluster.clickhouse.versionProbe.resources`.

### Query endpoint

Query clients — Grafana, ClickStack, `clickhouse-client`, anything running a
`SELECT` — connect to a ClusterIP Service the chart creates:

```
HTTP    <cluster-name>-clickhouse-client.<namespace>.svc:8123
Native  <cluster-name>-clickhouse-client.<namespace>.svc:9000
```

With `cluster.tls.enabled`, those become `8443` / `9440`, and the plaintext pair
disappears once `tls.required` is also set — the same rule the operator applies
to the server's own listeners.

This exists because the operator's `<cluster-name>-clickhouse-headless` Service
is not a client endpoint. It is headless, so there is no VIP and a client
resolves it straight to Pod IPs; a connection pool then holds those IPs and
keeps using a Pod after it goes unhealthy. And the operator sets
`publishNotReadyAddresses: true` on it, so its DNS deliberately hands out Pods
that are still starting. The ClusterIP Service selects the same Pods
(labels set by the Altinity operator, e.g. `clickhouse.altinity.com/chi`, `clickhouse.altinity.com/app=chop`) with
kube-proxy in front, so only ready endpoints receive traffic and liveness is
re-checked per connection rather than at DNS-resolution time.

Two things it does not do:

- **Reach outside the cluster.** ClusterIP is in-cluster only. Grafana Cloud or
  a Grafana in another cluster needs an Ingress or a LoadBalancer; this chart
  creates neither.
- **Survive sharding.** With `shards > 1` it load-balances across shards, and
  each query returns whichever shard answered. Same trap as the rotel write
  path — see [Sharding is not a values-only change](#sharding-is-not-a-values-only-change).

Round-robin across replicas is correct at `shards: 1` (every replica holds the
full dataset) but not deterministic: replicas sit at different points in
replication, so a dashboard comparing values across refreshes can watch a
counter go backwards. `cluster.clickhouse.service.sessionAffinity: ClientIP`
pins each client to one replica. Set `cluster.clickhouse.service.enabled: false`
to drop the Service and go back to the headless name.

### Visualization

ClickHouse **26.2+** embeds the ClickStack (HyperDX) UI in the server binary at
`http://<cluster-name>-clickhouse-client.<namespace>.svc:8123/clickstack` —
auto-detects the `otel_*` tables, gives
search, trace waterfalls, chart explorer, and service maps with zero extra
components. No persistence for dashboards/alerts (browser-local state) — good
for dev/small teams; for full ClickStack (alerts, saved dashboards, auth) run
[HyperDX + MongoDB](https://clickhouse.com/docs/use-cases/observability/clickstack/deployment)
against this cluster, or use Grafana with the ClickHouse datasource.

## Scaling path (when the business grows)

1. Raise `cluster.clickhouse.resources` (vertical)
2. Raise `cluster.clickhouse.replicas` past the default `3`
3. Grow PVC size (needs expandable StorageClass)
4. Shorten `cluster.rotel.exporter.ttl` before adding capacity for data you do
   not query
5. Enable TLS + network policies
6. Add shards — **only together with the Distributed-table work below**

### Sharding is not a values-only change

`cluster.clickhouse.shards` defaults to `1` and should stay there until the
dataset genuinely outgrows one node. Raising it on its own produces wrong
query results, silently:

- The operator gives each shard its own Keeper path (`/clickhouse/tables/{uuid}/{shard}`),
  so shards replicate independently and never exchange rows.
- Rotel writes to the headless Service, whose DNS round-robins across every
  pod, so rows land in whichever shard answered.
- `otel.otel_traces` is a plain `ReplicatedMergeTree`. A query reads the local
  table only, so it returns one shard's rows — no error, no warning.

`rotel-clickhouse-ddl` cannot create the missing piece (`--engine` accepts only
`MergeTree`, `ReplicatedMergeTree`, `Null`), so sharding means adding a
`Distributed` layer to this chart:

- Have the DDL Job build the local tables under a separate prefix and create
  `otel.otel_traces` as `Distributed(default, otel, <local>, cityHash64(TraceId))`,
  so the name rotel writes and ClickStack auto-detects is the correct one.
  Hashing on `TraceId` keeps one trace's spans on a single shard.
- Point the TTL Job at the local tables: `Distributed` rejects `MODIFY TTL`,
  mutations and `OPTIMIZE`.
- Point the user grants and row filters at the Distributed table. Policies do
  apply through it, but a granted table with no filter returns every user's
  rows, so both have to move together.

Adding the layer later is cheap: `RENAME TABLE` is a metadata-only operation,
so the migration is a rename plus a `CREATE TABLE`, not a data copy.

## Uninstall

```bash
helm uninstall ch-aio -n clickhouse
# PVCs are retained by default (reclaimPolicy: Retain) — delete carefully
kubectl delete pvc -n clickhouse -l app.kubernetes.io/instance=ch-aio
# CRDs are cluster-scoped and survive uninstall (operator crdHook / Helm crds/)
```

## References

- [Altinity Operator Quick Start](https://github.com/Altinity/clickhouse-operator/blob/master/docs/quick_start.md)
- [Altinity Operator docs](https://docs.altinity.com/altinitykubernetesoperator/)
- [Altinity Helm charts](https://github.com/Altinity/helm-charts/tree/main/charts/clickhouse)
- [Rotel](https://github.com/streamfold/rotel) — collector and ClickHouse exporter
