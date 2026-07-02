package provision

// Templates des fichiers d'installation OpenShift 4.x.
// Méthode privilégiée : Agent-based Installer (bare-metal / air-gapped).

const installConfigTmpl = `apiVersion: v1
baseDomain: {{ .BaseDomain }}
metadata:
  name: {{ .ClusterName }}
controlPlane:
  name: master
  replicas: {{ .Masters }}
  architecture: amd64
  hyperthreading: Enabled
compute:
- name: worker
  replicas: {{ .Workers }}
  architecture: amd64
  hyperthreading: Enabled
networking:
  networkType: OVNKubernetes
  clusterNetwork:
  - cidr: {{ .ClusterNetwork }}
    hostPrefix: {{ .HostPrefix }}
  machineNetwork:
  - cidr: {{ .MachineNetwork }}
  serviceNetwork:
  - {{ .ServiceNetwork }}
platform:
{{- if eq .Platform "baremetal" }}
  baremetal:
    apiVIPs:
    - {{ .APIVIP }}
    ingressVIPs:
    - {{ .IngressVIP }}
{{- else if eq .Platform "vsphere" }}
  # Compléter avec les paramètres vCenter (voir README-install.md)
  vsphere: {}
{{- else }}
  none: {}
{{- end }}
pullSecret: '{{ .PullSecret }}'
sshKey: '{{ .SSHKey }}'
{{- if .MirrorRegistry }}
imageDigestSources:
- mirrors:
  - {{ .MirrorRegistry }}/openshift/release
  source: quay.io/openshift-release-dev/ocp-v4.0-art-dev
- mirrors:
  - {{ .MirrorRegistry }}/openshift/release-images
  source: quay.io/openshift-release-dev/ocp-release
{{- if .TrustBundle }}
additionalTrustBundle: |
{{ .TrustBundle }}
{{- end }}
{{- end }}
`

const agentConfigTmpl = `apiVersion: v1beta1
kind: AgentConfig
metadata:
  name: {{ .ClusterName }}
rendezvousIP: {{ .RendezvousIP }}
{{- if .NTPServers }}
additionalNTPSources:
{{- range .NTPServers }}
- {{ . }}
{{- end }}
{{- end }}
# hosts: — décommenter et compléter, 1 entrée par nœud (voir README-install.md).
# La configuration réseau statique (NMState) et le mapping MAC/interface se
# déclarent ici pour l'Agent-based Installer.
#
# hosts:
#{{- range .HostSkeleton }}
# - hostname: {{ . }}
#   role: master
#   interfaces:
#   - name: eno1
#     macAddress: "AA:BB:CC:DD:EE:FF"   # ← MAC réelle du nœud
#{{- end }}
`

const installReadmeTmpl = `# Installation du cluster {{ .ClusterName }}.{{ .BaseDomain }}

Fichiers générés par ocp-toolkit (module provision) — méthode **Agent-based Installer**.

## Prérequis

- Binaires : ` + "`openshift-install`" + ` (même version mineure que le cluster cible) et ` + "`oc`" + `.
- Pull-secret valide (console.redhat.com → Downloads) — déjà intégré à install-config.yaml.
- DNS : enregistrements ` + "`api.{{ .ClusterName }}.{{ .BaseDomain }}`" + ` et ` + "`*.apps.{{ .ClusterName }}.{{ .BaseDomain }}`" + `.
{{- if eq .Platform "baremetal" }}
- VIPs réservées : API {{ .APIVIP }}, Ingress {{ .IngressVIP }} (gérées par keepalived intégré, pas de LB externe requis en baremetal).
{{- else }}
- Load balancer externe pour api:6443, machine-config:22623 et ingress 80/443 (plateforme "none").
{{- end }}
{{- if .MirrorRegistry }}
- Registre miroir accessible : {{ .MirrorRegistry }} (images synchronisées via ` + "`oc mirror`" + `).
{{- end }}
- NTP fonctionnel sur tous les nœuds (dérive d'horloge = échec d'install classique).

## ⚠ Sécurité

` + "`install-config.yaml`" + ` contient le **pull-secret** et est consommé (supprimé) par
openshift-install : garder une copie hors Git. Un .gitignore est fourni.

## Étapes

1. Compléter la section ` + "`hosts:`" + ` de ` + "`agent-config.yaml`" + ` (MAC, rôles, réseau statique éventuel).
2. Générer l'ISO d'installation :
   ` + "```" + `
   openshift-install agent create image --dir .
   ` + "```" + `
3. Booter les {{ .Masters }} masters (+ {{ .Workers }} workers) sur l'ISO. Le nœud {{ .RendezvousIP }} est le rendezvous.
4. Suivre l'installation :
   ` + "```" + `
   openshift-install agent wait-for bootstrap-complete --dir .
   openshift-install agent wait-for install-complete --dir .
   ` + "```" + `
5. Récupérer le kubeconfig dans ` + "`auth/kubeconfig`" + ` puis valider les nœuds :
   ` + "```" + `
   ocptk validate --confirm
   ` + "```" + `
`

const gitignoreContent = `# Générés par ocp-toolkit — contiennent le pull-secret / des identifiants.
install-config.yaml
.openshift_install_state.json
auth/
*.iso
`
