{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "kubevela.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kubevela.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "kubevela.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "kubevela.labels" -}}
helm.sh/chart: {{ include "kubevela.chart" . }}
{{ include "kubevela.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "kubevela.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubevela.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "kubevela-cluster-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubevela.name" . }}-cluster-gateway
app.kubernetes.io/instance: {{ .Release.Name }}-cluster-gateway
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "kubevela.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
    {{ default (include "kubevela.fullname" .) .Values.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/*
Name of the service account vela-cluster-core runs as.

This is the dedicated account the cluster-core ClusterRole and Role are bound to, so the pod
always runs with those least-privilege rules rather than inheriting the main controller's
account (which is bound to cluster-admin unless authentication.enabled is set). It is not
conditional on any cloud setting: clusterCore.aws.serviceAccountRoleArn only annotates this
account for IRSA, it does not select which account is used.

When serviceAccount.create is false the chart creates no accounts at all, so there is nothing
dedicated to run as and the configured account is used instead.
*/}}
{{- define "kubevela.clusterCoreServiceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
    {{ include "kubevela.fullname" . }}-cluster-core
{{- else -}}
    {{ include "kubevela.serviceAccountName" . }}
{{- end -}}
{{- end -}}

{{/*
systemDefinitionNamespace value defaulter
*/}}
{{- define "systemDefinitionNamespace" -}}
{{- if .Values.systemDefinitionNamespace -}}
    {{ .Values.systemDefinitionNamespace }}
{{- else -}}
    {{ .Release.Namespace }}
{{- end -}}
{{- end -}}
{{/*
Credential cache TTL for vela-cluster-core, in seconds, validated on the way out.

Emits the value so callers get validation for free by using it: there is no way to render
the flag without passing the check. 900 seconds is the EKS presigned-token window, and a
spoke credential is served until the earlier of this ceiling and its own refresh deadline
(the AWS arm reports that deadline 13 minutes out), so anything above 900 can never take
effect. Failing the install beats rendering a value the controller will silently ignore.

Zero and below are allowed and disable caching, which restores per-reconcile
materialization.
*/}}
{{- define "kubevela.clusterCore.credentialCacheTTLSeconds" -}}
{{- $ttl := int .Values.clusterCore.credentialCacheTTLSeconds -}}
{{- if gt $ttl 900 -}}
{{- fail (printf "clusterCore.credentialCacheTTLSeconds is %d, which exceeds the maximum of 900 (15 minutes). AWS spoke credentials expire after 900 seconds, so a longer TTL can never take effect. Use 900 or less, or 0 to disable credential caching." $ttl) -}}
{{- end -}}
{{- $ttl -}}
{{- end -}}
