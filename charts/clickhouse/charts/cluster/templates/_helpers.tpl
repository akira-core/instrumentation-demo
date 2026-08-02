{{/*
Expand the name of the chart.

Feeds app.kubernetes.io/name, which is part of the selector labels below, so
nameOverride is an install-time decision: a Deployment's spec.selector is
immutable and an upgrade that changes it is rejected outright.
*/}}
{{- define "cluster.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.

Base name for CHI/CHK CRs, rotel Deployment/Service/Jobs, and the generated
password Secret. Official Altinity examples often give CHI and CHK matching
names; the chart does the same by default.
*/}}
{{- define "cluster.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
ClickHouseInstallation (CHI) resource name.
*/}}
{{- define "cluster.clickhouseName" -}}
{{- default (include "cluster.fullname" .) .Values.clickhouse.name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
ClickHouseKeeperInstallation (CHK) resource name.
*/}}
{{- define "cluster.keeperName" -}}
{{- default (include "cluster.fullname" .) .Values.keeper.name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Internal Keeper cluster name inside the CHK (configuration.clusters[].name).
*/}}
{{- define "cluster.keeperClusterName" -}}
{{- default "cluster1" .Values.keeper.clusterName | trunc 15 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "cluster.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "cluster.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "cluster.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cluster.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Default user password secret name
*/}}
{{- define "cluster.passwordSecretName" -}}
{{- if .Values.clickhouse.defaultUser.existingSecret }}
{{- .Values.clickhouse.defaultUser.existingSecret }}
{{- else }}
{{- printf "%s-default-password" (include "cluster.clickhouseName" .) }}
{{- end }}
{{- end }}

{{/*
TLS certificate secret names
*/}}
{{- define "cluster.clickhouseTlsSecretName" -}}
{{- if and .Values.tls.enabled .Values.tls.clickhouse.existingSecret }}
{{- .Values.tls.clickhouse.existingSecret }}
{{- else }}
{{- printf "%s-clickhouse-tls" (include "cluster.clickhouseName" .) }}
{{- end }}
{{- end }}

{{- define "cluster.keeperTlsSecretName" -}}
{{- if and .Values.tls.enabled .Values.tls.keeper.existingSecret }}
{{- .Values.tls.keeper.existingSecret }}
{{- else }}
{{- printf "%s-keeper-tls" (include "cluster.keeperName" .) }}
{{- end }}
{{- end }}

{{/*
Labels merged into CR metadata and pod templates. Component labels win over
commonLabels on a key collision.
*/}}
{{- define "cluster.operatorResourceLabels" -}}
{{- include "cluster.stringMap" (merge (dict) (.extra | default dict) (.root.Values.commonLabels | default dict)) -}}
{{- end }}

{{/*
Render a map with every value coerced to a string.
*/}}
{{- define "cluster.stringMap" -}}
{{- $out := dict -}}
{{- range $k, $v := . -}}
{{- $_ := set $out $k (toString $v) -}}
{{- end -}}
{{- with $out }}{{ toYaml . }}{{ end }}
{{- end }}

{{/*
Flatten a nested map into Altinity slash-path settings keys.

  {logger: {level: information}}  ->  logger/level: information

Scalars print as key: value. Lists print as YAML sequences under the path.
*/}}
{{- define "cluster.flattenSettings" -}}
{{- include "cluster.flattenSettingsWalk" (dict "prefix" "" "data" .) -}}
{{- end }}

{{- define "cluster.flattenSettingsWalk" -}}
{{- $prefix := .prefix -}}
{{- range $k, $v := .data -}}
  {{- $path := $k -}}
  {{- if $prefix }}{{ $path = printf "%s/%s" $prefix $k }}{{ end -}}
  {{- if kindIs "map" $v -}}
    {{- include "cluster.flattenSettingsWalk" (dict "prefix" $path "data" $v) -}}
  {{- else if kindIs "slice" $v -}}
{{ $path }}:
{{- range $item := $v }}
  - {{ $item | toString | quote }}
{{- end }}
  {{- else -}}
    {{- /* Avoid scientific notation on large ints parsed as float64; keep
         trailing newline — {{- end -}} would eat it and glue keys together. */ -}}
{{ printf "%s: %s\n" $path (include "cluster.configValue" $v | quote) }}
  {{- end }}
{{- end -}}
{{- end }}

{{/*
Format a scalar for Altinity config.
Large whole numbers must not become scientific notation (1e+09); ratios like
0.9 must keep their fractional part.
*/}}
{{- define "cluster.configValue" -}}
{{- if kindIs "float64" . -}}
{{- if eq (floor .) . -}}
{{- printf "%.0f" . -}}
{{- else -}}
{{- printf "%g" . -}}
{{- end -}}
{{- else -}}
{{- . | toString -}}
{{- end -}}
{{- end }}

{{/*
Convert clickhouse.settings.extraUsersConfig.users (nested) into Altinity
configuration.users flat keys.

Supported per-user fields:
  password / passwordSecret{name,key} / password_sha256_hex
  profile, quota, networks.ip (string or list)
  grants.query (list) or grants (list of strings)
  databases.<db>.<table>.filter  -> row policy as files is out of scope;
    filters are emitted as users.d XML via a simplified path when present:
    user/databases/db/table/filter is NOT standard Altinity slash syntax for
    row policies — Altinity uses grants + settings. For row filters we emit
    <user>/databases/... only if the operator accepts it; prefer grants.
*/}}
{{- define "cluster.altinityUsers" -}}
{{- $users := dig "extraUsersConfig" "users" (dict) .Values.clickhouse.settings -}}
{{- range $user, $cfg := $users -}}
{{- if $cfg -}}
  {{- if and $cfg.passwordSecret $cfg.passwordSecret.name }}
{{ $user }}/password:
  valueFrom:
    secretKeyRef:
      name: {{ $cfg.passwordSecret.name | quote }}
      key: {{ $cfg.passwordSecret.key | default "password" | quote }}
  {{- else if and $cfg.password (kindIs "map" $cfg.password) (index $cfg.password "@from_env") }}
    {{- $envName := index $cfg.password "@from_env" -}}
    {{- $secretName := "" -}}
    {{- $secretKey := "password" -}}
    {{- range $.Values.clickhouse.containerTemplate.env | default list }}
      {{- if and (eq .name $envName) .valueFrom .valueFrom.secretKeyRef }}
        {{- $secretName = .valueFrom.secretKeyRef.name -}}
        {{- $secretKey = .valueFrom.secretKeyRef.key | default "password" -}}
      {{- end }}
    {{- end }}
    {{- if $secretName }}
{{ $user }}/password:
  valueFrom:
    secretKeyRef:
      name: {{ $secretName | quote }}
      key: {{ $secretKey | quote }}
    {{- else }}
      {{- fail (printf "user %q: password @from_env %q has no matching containerTemplate.env secretKeyRef — set passwordSecret.name or add the env entry" $user $envName) }}
    {{- end }}
  {{- else if and $cfg.password (kindIs "string" $cfg.password) }}
{{ $user }}/password: {{ $cfg.password | quote }}
  {{- else if $cfg.password_sha256_hex }}
{{ $user }}/password_sha256_hex: {{ $cfg.password_sha256_hex | quote }}
  {{- end }}
  {{- with $cfg.profile }}
{{ $user }}/profile: {{ . | quote }}
  {{- end }}
  {{- with $cfg.quota }}
{{ $user }}/quota: {{ . | quote }}
  {{- end }}
  {{- with $cfg.networks }}
    {{- $ip := .ip }}
    {{- if kindIs "slice" $ip }}
{{ $user }}/networks/ip:
      {{- range $ip }}
  - {{ . | quote }}
      {{- end }}
    {{- else if $ip }}
{{ $user }}/networks/ip: {{ $ip | quote }}
    {{- end }}
  {{- end }}
  {{- $grantList := list -}}
  {{- if and $cfg.grants $cfg.grants.query }}
    {{- $grantList = $cfg.grants.query -}}
  {{- else if and $cfg.grants (kindIs "slice" $cfg.grants) }}
    {{- $grantList = $cfg.grants -}}
  {{- end }}
  {{- if $grantList }}
{{ $user }}/grants/query:
    {{- range $grantList }}
  - {{ . | quote }}
    {{- end }}
  {{- end }}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Client-facing ClickHouse Service name (CHI serviceTemplate generateName).
*/}}
{{- define "cluster.clickhouseServiceName" -}}
{{- if .Values.clickhouse.service.name }}
{{- .Values.clickhouse.service.name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-clickhouse-client" (include "cluster.clickhouseName" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Rotel collector resource name
*/}}
{{- define "cluster.rotelName" -}}
{{- if .Values.rotel.name }}
{{- .Values.rotel.name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-rotel" (include "cluster.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Pod metadata for the rotel schema/retention Jobs.
*/}}
{{- define "cluster.rotelJobPodMetadata" -}}
{{- $jobs := .root.Values.rotel.jobs | default dict -}}
metadata:
  labels:
    {{- include "cluster.selectorLabels" .root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
    {{- with $jobs.podLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  {{- with $jobs.podAnnotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}

{{/*
Scheduling block shared by the rotel schema/retention Jobs.
*/}}
{{- define "cluster.rotelJobScheduling" -}}
{{- $jobs := .Values.rotel.jobs | default dict -}}
{{- with $jobs.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $jobs.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $jobs.affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}

{{/*
ClickHouse service host clients and rotel should use (ClusterIP client service).
*/}}
{{- define "cluster.clickhouseHost" -}}
{{- printf "%s.%s.svc.cluster.local" (include "cluster.clickhouseServiceName" .) .Release.Namespace }}
{{- end }}

{{/*
HTTP endpoint rotel uses to reach ClickHouse
*/}}
{{- define "cluster.rotelClickhouseEndpoint" -}}
{{- if .Values.rotel.exporter.endpoint }}
{{- .Values.rotel.exporter.endpoint }}
{{- else }}
{{- printf "http://%s:8123" (include "cluster.clickhouseHost" .) }}
{{- end }}
{{- end }}

{{/*
Fully-qualified image reference.
*/}}
{{- define "cluster.image" -}}
{{- $repository := include "cluster.imageRepository" . -}}
{{- with .tag -}}
{{- printf "%s:%s" $repository (. | toString) -}}
{{- else -}}
{{- $repository -}}
{{- end -}}
{{- end }}

{{/*
Registry-qualified repository, without the tag.
*/}}
{{- define "cluster.imageRepository" -}}
{{- $registry := .registry | default "" | toString | trimSuffix "/" -}}
{{- if $registry -}}
{{- printf "%s/%s" $registry .repository -}}
{{- else -}}
{{- .repository -}}
{{- end -}}
{{- end }}

{{/*
Table prefix for one signal.
*/}}
{{- define "cluster.rotelTablePrefix" -}}
{{- $e := .root.Values.rotel.exporter -}}
{{- $base := $e.tablePrefix | default "otel" -}}
{{- $p := dig .signal "tablePrefix" "" $e | toString | default $base -}}
{{- if not (regexMatch "^[A-Za-z_][A-Za-z0-9_]*$" $p) -}}
{{- fail (printf "rotel.exporter.%s.tablePrefix %q: must be a ClickHouse identifier matching ^[A-Za-z_][A-Za-z0-9_]*$" .signal $p) -}}
{{- end -}}
{{- $p -}}
{{- end }}

{{/*
ClickHouse exporter groups for rotel's multi-exporter layout.
*/}}
{{- define "cluster.rotelExporterGroups" -}}
{{- $telemetry := .Values.rotel.telemetry -}}
{{- $order := list -}}
{{- $groups := dict -}}
{{- range $signal := list "traces" "logs" "metrics" -}}
  {{- if index $telemetry $signal -}}
    {{- $prefix := include "cluster.rotelTablePrefix" (dict "root" $ "signal" $signal) -}}
    {{- if not (hasKey $groups $prefix) -}}
      {{- $order = append $order $prefix -}}
      {{- $_ := set $groups $prefix list -}}
    {{- end -}}
    {{- $_ := set $groups $prefix (append (index $groups $prefix) $signal) -}}
  {{- end -}}
{{- end -}}
{{- $out := list -}}
{{- range $prefix := $order -}}
{{- $out = append $out (dict "name" (printf "ch_%s" $prefix) "prefix" $prefix "signals" (index $groups $prefix)) -}}
{{- end -}}
{{- toYaml $out -}}
{{- end }}

{{/*
Env block for one ClickHouse exporter.
*/}}
{{- define "cluster.rotelClickhouseExporterEnv" -}}
{{- $root := .root -}}
{{- $var := printf "ROTEL_EXPORTER_%s" (upper .name) -}}
{{- $async := $root.Values.rotel.exporter.asyncInsert | toString | lower -}}
{{- if not (has $async (list "true" "false" "1" "0")) -}}
{{- fail (printf "rotel.exporter.asyncInsert %q: must be true or false" $root.Values.rotel.exporter.asyncInsert) -}}
{{- end -}}
{{- $asyncInsert := ternary "True" "False" (has $async (list "true" "1")) -}}
- name: {{ $var }}_ENDPOINT
  value: {{ include "cluster.rotelClickhouseEndpoint" $root | quote }}
- name: {{ $var }}_DATABASE
  value: {{ $root.Values.rotel.exporter.database | quote }}
- name: {{ $var }}_TABLE_PREFIX
  value: {{ .prefix | quote }}
- name: {{ $var }}_USER
  value: {{ $root.Values.rotel.exporter.user | quote }}
- name: {{ $var }}_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "cluster.rotelPasswordSecretName" $root | quote }}
      key: {{ include "cluster.rotelPasswordSecretKey" $root | quote }}
- name: {{ $var }}_COMPRESSION
  value: {{ $root.Values.rotel.exporter.compression | quote }}
- name: {{ $var }}_ASYNC_INSERT
  value: {{ $asyncInsert | quote }}
{{- if $root.Values.rotel.exporter.enableJson }}
- name: {{ $var }}_ENABLE_JSON
  value: "true"
{{- end }}
{{- end }}

{{/*
Secret holding the password rotel authenticates with
*/}}
{{- define "cluster.rotelPasswordSecretName" -}}
{{- if .Values.rotel.exporter.existingSecret }}
{{- .Values.rotel.exporter.existingSecret }}
{{- else }}
{{- include "cluster.passwordSecretName" . }}
{{- end }}
{{- end }}

{{- define "cluster.rotelPasswordSecretKey" -}}
{{- if .Values.rotel.exporter.existingSecret }}
{{- .Values.rotel.exporter.existingSecretKey | default "password" }}
{{- else }}
{{- .Values.clickhouse.defaultUser.existingSecretKey | default "password" }}
{{- end }}
{{- end }}

{{/*
Whether the otel database uses the Replicated engine.
*/}}
{{- define "cluster.rotelDatabaseReplicated" -}}
{{- $e := .Values.rotel.exporter.databaseEngine | default "Atomic" -}}
{{- if not (has $e (list "Replicated" "Atomic")) -}}
{{- fail (printf "rotel.exporter.databaseEngine %q: must be Replicated or Atomic" $e) -}}
{{- end -}}
{{- if eq $e "Replicated" -}}
{{- if not .Values.rotel.exporter.cluster -}}
{{- fail "rotel.exporter.databaseEngine=Replicated needs rotel.exporter.cluster set — every host has to join the database by name for the DDL log to reach it" -}}
{{- end -}}
{{- if ne .Values.rotel.exporter.engine "ReplicatedMergeTree" -}}
{{- fail (printf "rotel.exporter.databaseEngine=Replicated with engine=%s: replicating DDL to hosts that each keep their own copy of the data is not replication. Use engine=ReplicatedMergeTree, or databaseEngine=Atomic for a single replica." .Values.rotel.exporter.engine) -}}
{{- end -}}
true
{{- end -}}
{{- end }}

{{/*
Keeper path holding the Replicated database's DDL log.
*/}}
{{- define "cluster.rotelDatabaseReplicaPath" -}}
{{- .Values.rotel.exporter.databaseReplicaPath | default (printf "/clickhouse/databases/%s" .Values.rotel.exporter.database) -}}
{{- end }}

{{/*
rotel.exporter.ttl as a whole number of seconds.
*/}}
{{- define "cluster.rotelTtlSeconds" -}}
{{- $ttl := .Values.rotel.exporter.ttl | toString -}}
{{- if not (regexMatch "^[0-9]+[smhd]$" $ttl) -}}
{{- fail (printf "rotel.exporter.ttl %q: expected a number followed by s, m, h or d (e.g. 168h, 30d, 0s)" $ttl) -}}
{{- end -}}
{{- $n := regexFind "^[0-9]+" $ttl | int64 -}}
{{- $unit := regexFind "[smhd]$" $ttl -}}
{{- if eq $unit "m" -}}{{- $n = mul $n 60 -}}
{{- else if eq $unit "h" -}}{{- $n = mul $n 3600 -}}
{{- else if eq $unit "d" -}}{{- $n = mul $n 86400 -}}
{{- end -}}
{{- $n -}}
{{- end }}

{{/*
ArgoCD sync-wave annotations, per component.

  1  certs
  2  keeper
  3  clickhouse (+ client service via CHI serviceTemplate)
  4  rotel

Usage: include "cluster.argocdAnnotations" "keeper"
*/}}
{{- define "cluster.argocdAnnotations" -}}
{{- $waves := dict "certs" 1 "keeper" 2 "pvc" 2 "clickhouse" 3 "clickhouse-service" 3 "rotel" 4 -}}
{{- if not (hasKey $waves .) }}
{{- fail (printf "cluster.argocdAnnotations: unknown component %q — expected one of %v" . (keys $waves | sortAlpha)) }}
{{- end -}}
argocd.argoproj.io/sync-wave: {{ index $waves . | quote }}
{{- if has . (list "certs" "keeper" "clickhouse") }}
argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true
{{- end }}
{{- end }}
