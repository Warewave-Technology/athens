{{- define "athens.name" -}}{{ .Chart.Name }}{{- end -}}

{{- define "athens.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}{{ .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else -}}{{ printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- end -}}

{{- define "athens.labels" -}}
app.kubernetes.io/name: {{ include "athens.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "athens.selectorLabels" -}}
app.kubernetes.io/name: {{ include "athens.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "athens.secretName" -}}
{{- default (include "athens.fullname" .) .Values.secret.existingSecret -}}
{{- end -}}

{{/* config.yaml with chart-managed fields (listen addresses, store path) */}}
{{- define "athens.config" -}}
{{- $cfg := deepCopy .Values.config -}}
{{- $server := default dict $cfg.server -}}
{{- $_ := set $server "listen" (printf ":%v" .Values.service.port) -}}
{{- $_ := set $server "metricsListen" (printf ":%v" .Values.service.metricsPort) -}}
{{- $_ := set $cfg "server" $server -}}
{{- $_ := set $cfg "store" (dict "path" "/data/athens.db") -}}
{{- toYaml $cfg -}}
{{- end -}}
