{{/*
Expand the name of the chart.
*/}}
{{- define "incident-controller.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "incident-controller.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "incident-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "incident-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "incident-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name
*/}}
{{- define "incident-controller.serviceAccountName" -}}
{{- default "incident-controller" .Values.serviceAccount.name }}
{{- end }}

{{/*
The check pod spec template the controller loads. The controller adds the script, the command
and the sandbox settings.
*/}}
{{- define "incident-controller.checkPod" -}}
serviceAccountName: {{ .Values.checks.serviceAccount }}
terminationGracePeriodSeconds: 5
securityContext:
  {{- toYaml .Values.checks.podSecurityContext | nindent 2 }}
{{- with .Values.checks.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.checks.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.checks.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
containers:
  - name: check
    image: "{{ .Values.checks.image.repository }}:{{ .Values.checks.image.tag | default .Chart.AppVersion }}"
    imagePullPolicy: {{ .Values.checks.image.pullPolicy }}
    resources:
      {{- toYaml .Values.checks.resources | nindent 6 }}
{{- end }}
