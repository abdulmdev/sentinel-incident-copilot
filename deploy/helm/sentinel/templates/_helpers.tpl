{{- define "sentinel.labels" -}}
app.kubernetes.io/name: sentinel
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "sentinel.selector" -}}
app.kubernetes.io/name: sentinel
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .name }}
{{- end }}

{{- define "sentinel.image" -}}
{{ .root.Values.image.registry }}/{{ .root.Values.image.prefix }}{{ .image | default .name }}:{{ .root.Values.image.tag }}
{{- end }}

{{- define "sentinel.secretName" -}}
{{ if .Values.secrets.create }}{{ .Release.Name }}-secrets{{ else }}{{ .Values.secrets.existingSecret }}{{ end }}
{{- end }}

{{- define "sentinel.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities: { drop: [ALL] }
{{- end }}

{{- define "sentinel.secretEnv" -}}
- name: {{ .env }}
  valueFrom: { secretKeyRef: { name: {{ include "sentinel.secretName" .root }}, key: {{ .key }} } }
{{- end }}

{{- define "sentinel.np" }}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ .root.Release.Name }}-allow-{{ .to }}
  labels: {{- include "sentinel.labels" .root | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/instance: {{ .root.Release.Name }}
      app.kubernetes.io/component: {{ .to }}
  policyTypes: [Ingress]
  ingress:
    - from:
        {{- range .from }}
        - podSelector:
            matchLabels:
              app.kubernetes.io/instance: {{ $.root.Release.Name }}
              app.kubernetes.io/component: {{ . }}
        {{- end }}
        {{- if has .to .root.Values.networkPolicy.externalAccess }}
        - ipBlock: { cidr: 0.0.0.0/0 }   # reachable from outside the cluster (LB / NodePort)
        {{- end }}
        {{- with .namespace }}
        - namespaceSelector:
            matchLabels: {{- toYaml . | nindent 14 }}
        {{- end }}
        - namespaceSelector:
            matchLabels: {{- toYaml .root.Values.networkPolicy.monitoringNamespaceLabel | nindent 14 }}
{{- end }}
