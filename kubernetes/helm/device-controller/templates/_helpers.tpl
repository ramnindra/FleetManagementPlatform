{{- define "device-controller.fullname" -}}
device-controller
{{- end -}}

{{- define "device-controller.labels" -}}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "device-controller.controllerSelectorLabels" -}}
app.kubernetes.io/name: device-controller
app.kubernetes.io/component: controller
{{- end -}}

{{- define "device-controller.emqxSelectorLabels" -}}
app.kubernetes.io/name: device-controller
app.kubernetes.io/component: emqx
{{- end -}}

{{- define "device-controller.postgresSelectorLabels" -}}
app.kubernetes.io/name: device-controller
app.kubernetes.io/component: postgres
{{- end -}}

{{- define "device-controller.namespace" -}}
{{ .Values.namespace | default .Release.Namespace }}
{{- end -}}

{{- define "device-controller.image" -}}
{{- if .Values.controller.image.digest -}}
{{ .Values.controller.image.repository }}@{{ .Values.controller.image.digest }}
{{- else -}}
{{ .Values.controller.image.repository }}:{{ .Values.controller.image.tag }}
{{- end -}}
{{- end -}}
