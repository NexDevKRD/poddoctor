{{- define "poddoctor-hub.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{- define "poddoctor-hub.fullname" -}}
{{- .Release.Name -}}
{{- end -}}

{{- define "poddoctor-hub.labels" -}}
app.kubernetes.io/name: {{ include "poddoctor-hub.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "poddoctor-hub.selectorLabels" -}}
app.kubernetes.io/name: {{ include "poddoctor-hub.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "poddoctor-hub.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- .Values.serviceAccount.name | default (include "poddoctor-hub.fullname" .) -}}
{{- else -}}
{{- .Values.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}

{{- define "poddoctor-hub.dsnSecretName" -}}
{{- .Values.database.existingSecret | default (printf "%s-db" (include "poddoctor-hub.fullname" .)) -}}
{{- end -}}

{{- define "poddoctor-hub.tokenSecretName" -}}
{{- .Values.auth.existingSecret | default (printf "%s-auth" (include "poddoctor-hub.fullname" .)) -}}
{{- end -}}
