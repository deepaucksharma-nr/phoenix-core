{{/*
Expand the name of the chart.
*/}}
{{- define "pte.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "pte.fullname" -}}
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
{{- define "pte.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "pte.labels" -}}
helm.sh/chart: {{ include "pte.chart" . }}
{{ include "pte.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "pte.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pte.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Generate environment variables from value
*/}}
{{- define "pte.envFromValue" -}}
- name: {{ .Name }}
  value: {{ .Value | quote }}
{{- end }}

{{/*
Generate environment variables from secret
*/}}
{{- define "pte.envFromSecret" -}}
- name: {{ .Name }}
  valueFrom:
    secretKeyRef:
      name: {{ .SecretName }}
      key: {{ .SecretKey }}
{{- end }}

{{/*
Data directory path for PVC
*/}}
{{- define "pte.dataDir" -}}
/data/pte
{{- end }}