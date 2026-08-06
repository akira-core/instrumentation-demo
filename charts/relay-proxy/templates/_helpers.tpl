{{/*
Expand the name of the chart.
*/}}
{{- define "relay-proxy.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "relay-proxy.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "relay-proxy.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "relay-proxy.labels" -}}
helm.sh/chart: {{ include "relay-proxy.chart" . }}
{{ include "relay-proxy.selectorLabels" . }}
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
{{- define "relay-proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "relay-proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "relay-proxy.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "relay-proxy.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding the relay proxy API keys. This is both the name of
the VaultSecret resource and the name of the Secret it materialises — the Vault
secret operators all name the generated Secret after the custom resource.
*/}}
{{- define "relay-proxy.apiKeySecretName" -}}
{{- if .Values.apiKeyAuth.vaultSecret.name }}
{{- tpl .Values.apiKeyAuth.vaultSecret.name . | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-api-key" (include "relay-proxy.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Environment variables injecting the API keys from the Secret above. The relay
proxy reads AUTHORIZEDKEYS_EVALUATION / AUTHORIZEDKEYS_ADMIN as comma-separated
lists and overlays them onto authorizedKeys.{evaluation,admin} in its config, so
the keys never have to be rendered into the ConfigMap.
Renders nothing when apiKeyAuth.enabled is false.
*/}}
{{- define "relay-proxy.apiKeyEnv" -}}
{{- if .Values.apiKeyAuth.enabled }}
{{- if not (or .Values.apiKeyAuth.evaluationSecretKey .Values.apiKeyAuth.adminSecretKey) }}
{{- fail "apiKeyAuth.enabled is true but neither apiKeyAuth.evaluationSecretKey nor apiKeyAuth.adminSecretKey is set: the relay proxy would come up with no authorized key and reject every call." }}
{{- end }}
{{- $secretName := include "relay-proxy.apiKeySecretName" . }}
{{- with .Values.apiKeyAuth.evaluationSecretKey }}
- name: {{ $.Values.apiKeyAuth.evaluationEnvVar }}
  valueFrom:
    secretKeyRef:
      name: {{ $secretName }}
      key: {{ . }}
{{- end }}
{{- with .Values.apiKeyAuth.adminSecretKey }}
- name: {{ $.Values.apiKeyAuth.adminEnvVar }}
  valueFrom:
    secretKeyRef:
      name: {{ $secretName }}
      key: {{ . }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Name of the ConfigMap built from the flag definition files in `flags.dir`. Kept
separate from the config ConfigMap so that editing a flag never rewrites the
relay proxy's own configuration.
*/}}
{{- define "relay-proxy.flagsConfigMapName" -}}
{{- printf "%s-flags" (include "relay-proxy.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Glob selecting the flag definition files shipped inside the chart. Only files
directly under `flags.dir` are picked up — sub-directories are not descended,
because the mounted ConfigMap is flat and two files of the same base name would
collide silently.
*/}}
{{- define "relay-proxy.flagsGlob" -}}
{{- printf "%s/*.{yaml,yml,json}" (trimSuffix "/" .Values.flags.dir) }}
{{- end }}

{{/*
The `retrievers` block appended to relayproxy.config: one `file` retriever per
flag file, pointing at the path flags-configmap.yaml is mounted on. GO Feature
Flag's file retriever takes a single file, not a directory, which is why this
has to be generated rather than hardcoded.
*/}}
{{- define "relay-proxy.flagRetrievers" -}}
{{- $glob := include "relay-proxy.flagsGlob" . }}
{{- $files := .Files.Glob $glob }}
{{- if not $files }}
{{- fail (printf "flags.enabled is true but no flag file matches %q inside the chart, so the relay proxy would start with nothing to serve and every evaluation would fall back to the caller's default. Add a flag file under %s/, point flags.dir somewhere else, or set flags.enabled=false." $glob (trimSuffix "/" .Values.flags.dir)) }}
{{- end }}
{{- $mountPath := trimSuffix "/" .Values.flags.mountPath }}
{{- $retrievers := list }}
{{- range $path, $_ := $files }}
{{- $retrievers = append $retrievers (dict "kind" "file" "path" (printf "%s/%s" $mountPath (base $path))) }}
{{- end -}}
retrievers:
{{ toYaml $retrievers | indent 2 }}
{{- end }}

{{/*
Renders a value that contains template
Usage:
{{ include "common.tplvalues.render" ( dict "value" .Values.path.to.the.Value "context" $ ) }}
*/}}
{{- define "relay-proxy.render" -}}
{{- $value := typeIs "string" .value | ternary .value (.value | toYaml) }}
{{- if contains "{{" (toJson .value) }}
  {{- tpl $value .context }}
{{- else }}
    {{- $value }}
{{- end }}
{{- end -}}

{{/*
Extract monitoringPort from relayproxy.config if it exists.
Supports server.monitoringPort (new format, nested or written as a literal
dotted key) and top-level monitoringPort (old format), preferring the former.

This reads the *parsed* config rather than pattern-matching its text: matching
text also matched commented-out lines, so a `# monitoringPort: 9999` left in the
config opened a container port nothing listens on and moved both probes onto it,
leaving the pod permanently not-Ready.
*/}}
{{- define "relay-proxy.monitoringPort" -}}
{{- if .Values.relayproxy.config }}
{{- $config := fromYaml (tpl (.Values.relayproxy.config | toString) .) }}
{{- $port := "" }}
{{- if kindIs "map" $config.server }}
{{- with (index $config.server "monitoringPort") }}{{ $port = . }}{{ end }}
{{- end }}
{{- if not $port }}{{ with (index $config "server.monitoringPort") }}{{ $port = . }}{{ end }}{{ end }}
{{- if not $port }}{{ with (index $config "monitoringPort") }}{{ $port = . }}{{ end }}{{ end }}
{{- with $port }}{{ . }}{{ end }}
{{- end }}
{{- end -}}
