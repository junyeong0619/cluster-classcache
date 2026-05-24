{{/* Common helpers */}}

{{- define "classcache.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "classcache.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "classcache.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "classcache.labels" -}}
app.kubernetes.io/name: {{ include "classcache.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "classcache.selectorLabels" -}}
app: classcache-operator
app.kubernetes.io/name: {{ include "classcache.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "classcache.webhookServiceName" -}}
classcache-webhook
{{- end -}}

{{- define "classcache.namespace" -}}
{{ .Release.Namespace }}
{{- end -}}
