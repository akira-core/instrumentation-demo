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
Extract monitoringPort from relayproxy.config if it exists
Supports both server.monitoringPort (new format) and monitoringPort (old format)
Prioritizes server.monitoringPort if both are present
*/}}
{{- define "relay-proxy.monitoringPort" -}}
{{- if .Values.relayproxy.config }}
{{- $config := .Values.relayproxy.config | toString }}
{{- $port := "" }}
{{- /* First check for server.monitoringPort (new format) */}}
{{- if contains "server.monitoringPort:" $config }}
{{- $serverPortMatch := regexFind "server\\.monitoringPort:\\s*(\\d+)" $config }}
{{- if $serverPortMatch }}
{{- /* Extract just the port number by removing everything before the digits */}}
{{- $port = regexReplaceAll ".*monitoringPort:\\s*" $serverPortMatch "" }}
{{- end }}
{{- end }}
{{- /* If not found, check for top-level monitoringPort (old format) */}}
{{- if not $port }}
{{- /* Replace server.monitoringPort temporarily to avoid matching it */}}
{{- $tempConfig := $config | replace "server.monitoringPort:" "SERVER_MONITORING_PORT_PLACEHOLDER:" }}
{{- if contains "monitoringPort:" $tempConfig }}
{{- $topLevelPortMatch := regexFind "monitoringPort:\\s*(\\d+)" $tempConfig }}
{{- if $topLevelPortMatch }}
{{- /* Extract just the port number by removing everything before the digits */}}
{{- $port = regexReplaceAll ".*monitoringPort:\\s*" $topLevelPortMatch "" }}
{{- end }}
{{- end }}
{{- end }}
{{- if $port }}
{{- $port | trim }}
{{- end }}
{{- end }}
{{- end -}}
