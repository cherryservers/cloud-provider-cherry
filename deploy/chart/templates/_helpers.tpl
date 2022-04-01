{{/*
Expand the name of the chart.
*/}}
{{- define "cloud-provider-cherry.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "cloud-provider-cherry.fullname" -}}
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
{{- define "cloud-provider-cherry.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "cloud-provider-cherry.labels" -}}
helm.sh/chart: {{ include "cloud-provider-cherry.chart" . }}
{{ include "cloud-provider-cherry.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "cloud-provider-cherry.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cloud-provider-cherry.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "cloud-provider-cherry.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "cloud-provider-cherry.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the secret containing the config file to use
*/}}
{{- define "cloud-provider-cherry.configSecretName" -}}
{{- if .Values.configSecret.create }}
{{- default (include "cloud-provider-cherry.fullname" .) .Values.configSecret.name }}
{{- else }}
{{- default "default" .Values.configSecret.name }}
{{- end }}
{{- end }}

{{/*
Create the image version tag
*/}}
{{- define "cloud-provider-cherry.imageTag" -}}
{{- if eq .Chart.AppVersion "master" }}
{{- default "master" .Values.image.tag }}
{{- else }}
{{- default .Values.image.tag }}
{{- end }}
{{- end }}
