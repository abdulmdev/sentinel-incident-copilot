{{- define "guard.name" -}}sentinel-guard{{- end -}}
{{- define "guard.labels" -}}
app.kubernetes.io/name: sentinel-guard
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- end -}}
{{- define "guard.selector" -}}
app.kubernetes.io/name: sentinel-guard
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "guard.policy" -}}
{{- $p := deepCopy .Values.policy -}}
{{- $_ := set $p "agents" .Values.agents -}}
{{ toYaml $p }}
{{- end -}}
{{/* CEL: true only for agent identities. has() guards keep a missing field from erroring (an error would fail closed for everyone). */}}
{{- define "guard.agentMatch" -}}
(has(request.userInfo.groups) && request.userInfo.groups.exists(g, g in {{ .Values.agents.groups | default list | toJson }})) || (has(request.userInfo.username) && request.userInfo.username in {{ .Values.agents.users | default list | toJson }})
{{- end -}}
