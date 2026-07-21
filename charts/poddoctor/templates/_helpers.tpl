{{- define "poddoctor.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{- define "poddoctor.fullname" -}}
{{- .Release.Name -}}
{{- end -}}

{{- define "poddoctor.labels" -}}
app.kubernetes.io/name: {{ include "poddoctor.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "poddoctor.selectorLabels" -}}
app.kubernetes.io/name: {{ include "poddoctor.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "poddoctor.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- .Values.serviceAccount.name | default (include "poddoctor.fullname" .) -}}
{{- else -}}
{{- .Values.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}
