package validate

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// Le DaemonSet de smoke-test : un pod par nœud, sous SCC restricted-v2
// (volontairement — vérifier que restricted-v2 s'applique EST un des checks).
// Le script imprime une ligne `OCPTK-RESULT {json}` puis reste vivant ;
// l'orchestrateur collecte via `oc logs` en parallèle. C'est la seule
// approche qui tient à 5000+ nœuds (aucun test séquentiel nœud par nœud).

// checksScript est exécuté dans chaque pod. Bash pur + /dev/tcp : aucune
// dépendance à des binaires absents d'ubi-minimal, et surtout aucun accès
// privilégié requis.
const checksScript = `#!/bin/bash
# ocp-toolkit — smoke-tests par noeud. Sortie: OCPTK-RESULT {json}
CHECKS=""
add() { CHECKS="${CHECKS}{\"name\":\"$1\",\"status\":\"$2\",\"detail\":\"$3\"},"; }
tcp() { timeout 5 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; }

# 1. Resolution DNS interne + reseau de services (kubernetes.default)
if tcp kubernetes.default.svc.cluster.local 443; then
  add "net-dns-service" "PASS" "resolution DNS + connexion kubernetes.default.svc:443 OK"
else
  add "net-dns-service" "FAIL" "echec resolution DNS ou connexion service kubernetes.default.svc:443"
fi

# 2. Pod-a-pod inter-noeuds via les IP des pods CoreDNS (port TCP 5353)
if [ -n "${DNS_POD_IPS}" ]; then
  P2P="FAIL"; DETAIL="aucune IP CoreDNS joignable parmi: ${DNS_POD_IPS}"
  for ip in ${DNS_POD_IPS}; do
    if tcp "${ip}" 5353; then P2P="PASS"; DETAIL="connexion pod-a-pod OK vers ${ip}:5353"; break; fi
  done
  add "net-pod-to-pod" "${P2P}" "${DETAIL}"
else
  add "net-pod-to-pod" "SKIP" "IP CoreDNS non fournies (cluster sans openshift-dns ?)"
fi

# 3. Registre interne OpenShift
if tcp image-registry.openshift-image-registry.svc.cluster.local 5000; then
  add "registry-internal" "PASS" "registre interne joignable sur :5000"
else
  add "registry-internal" "WARN" "registre interne injoignable (absent ou non expose)"
fi

# 4. Egress sortant (optionnel, cible fournie par --egress-target)
if [ -n "${EGRESS_TARGET}" ]; then
  H="${EGRESS_TARGET%:*}"; P="${EGRESS_TARGET##*:}"
  if tcp "${H}" "${P}"; then
    add "net-egress" "PASS" "egress OK vers ${EGRESS_TARGET}"
  else
    add "net-egress" "FAIL" "egress KO vers ${EGRESS_TARGET}"
  fi
else
  add "net-egress" "SKIP" "pas de cible egress configuree (--egress-target host:port)"
fi

# 5. SELinux : contexte du process conteneur (container_t => enforcing effectif)
CTX=$(cat /proc/self/attr/current 2>/dev/null)
case "${CTX}" in
  *container_t*) add "selinux-context" "PASS" "contexte SELinux: ${CTX}" ;;
  "")            add "selinux-context" "FAIL" "SELinux invisible ou desactive sur ce noeud" ;;
  *)             add "selinux-context" "WARN" "contexte SELinux inattendu: ${CTX}" ;;
esac

# 6. SCC effectivement appliquee au pod (via downward API)
SCC=$(grep -o 'openshift.io/scc="[^"]*"' /etc/podinfo/annotations 2>/dev/null | cut -d'"' -f2)
case "${SCC}" in
  restricted-v2) add "scc-restricted-v2" "PASS" "SCC appliquee: ${SCC}" ;;
  "")            add "scc-restricted-v2" "FAIL" "annotation openshift.io/scc absente (noeud/cluster non OpenShift ?)" ;;
  *)             add "scc-restricted-v2" "WARN" "SCC appliquee: ${SCC} (attendu: restricted-v2)" ;;
esac

echo "OCPTK-RESULT {\"node\":\"${NODE_NAME}\",\"checks\":[${CHECKS%,}]}"
# Reste vivant : permet un diagnostic manuel (oc exec / oc logs) apres coup.
sleep infinity
`

const dsTemplate = `apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Namespace }}
  labels:
    app.kubernetes.io/managed-by: ocp-toolkit
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ocptk-validate
  namespace: {{ .Namespace }}
  labels:
    app.kubernetes.io/managed-by: ocp-toolkit
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ocptk-checks
  namespace: {{ .Namespace }}
  labels:
    app.kubernetes.io/managed-by: ocp-toolkit
data:
  checks.sh: |
{{ .ScriptIndented }}
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: ocptk-validate
  namespace: {{ .Namespace }}
  labels:
    app.kubernetes.io/managed-by: ocp-toolkit
spec:
  selector:
    matchLabels:
      app: ocptk-validate
  template:
    metadata:
      labels:
        app: ocptk-validate
        app.kubernetes.io/managed-by: ocp-toolkit
    spec:
      serviceAccountName: ocptk-validate
      priorityClassName: ""
      terminationGracePeriodSeconds: 5
{{- if .TolerateAll }}
      tolerations:
      - operator: Exists
{{- end }}
{{- if .NodeSelector }}
      nodeSelector:
{{- range $k, $v := .NodeSelector }}
        {{ $k }}: "{{ $v }}"
{{- end }}
{{- end }}
      containers:
      - name: checks
        image: {{ .Image }}
        command: ["/bin/bash", "/opt/ocptk/checks.sh"]
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: DNS_POD_IPS
          value: "{{ .DNSPodIPs }}"
        - name: EGRESS_TARGET
          value: "{{ .EgressTarget }}"
        resources:
          requests:
            cpu: 10m
            memory: 32Mi
          limits:
            cpu: 100m
            memory: 64Mi
        securityContext:
          allowPrivilegeEscalation: false
          runAsNonRoot: true
          capabilities:
            drop: ["ALL"]
          seccompProfile:
            type: RuntimeDefault
        volumeMounts:
        - name: script
          mountPath: /opt/ocptk
        - name: podinfo
          mountPath: /etc/podinfo
      volumes:
      - name: script
        configMap:
          name: ocptk-checks
          defaultMode: 0555
      - name: podinfo
        downwardAPI:
          items:
          - path: annotations
            fieldRef:
              fieldPath: metadata.annotations
`

// pvcTemplate — test de stockage échantillonné (jamais 1 PVC par nœud :
// 5000 PVC simultanés mettraient le backend de stockage à genoux).
const pvcTemplate = `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{ .Name }}
  namespace: {{ .Namespace }}
  labels:
    app.kubernetes.io/managed-by: ocp-toolkit
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: {{ .StorageClass }}
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: {{ .Name }}
  namespace: {{ .Namespace }}
  labels:
    app.kubernetes.io/managed-by: ocp-toolkit
spec:
  nodeName: {{ .Node }}
  restartPolicy: Never
  terminationGracePeriodSeconds: 5
  containers:
  - name: probe
    image: {{ .Image }}
    command: ["/bin/bash", "-c", "echo ocptk > /mnt/test/probe && cat /mnt/test/probe >/dev/null && echo PVC-OK"]
    resources:
      requests: {cpu: 10m, memory: 32Mi}
      limits: {cpu: 100m, memory: 64Mi}
    securityContext:
      allowPrivilegeEscalation: false
      runAsNonRoot: true
      capabilities: {drop: ["ALL"]}
      seccompProfile: {type: RuntimeDefault}
    volumeMounts:
    - name: data
      mountPath: /mnt/test
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: {{ .Name }}
`

type dsParams struct {
	Namespace      string
	Image          string
	DNSPodIPs      string
	EgressTarget   string
	TolerateAll    bool
	NodeSelector   map[string]string
	ScriptIndented string
}

func renderManifests(p dsParams) ([]byte, error) {
	// Indente le script de 4 espaces pour l'insérer dans le ConfigMap.
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(checksScript, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
		} else {
			b.WriteString("    " + line + "\n")
		}
	}
	p.ScriptIndented = strings.TrimRight(b.String(), "\n")

	t, err := template.New("ds").Parse(dsTemplate)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type pvcParams struct {
	Name         string
	Namespace    string
	Node         string
	Image        string
	StorageClass string
}

func renderPVCProbe(p pvcParams) ([]byte, error) {
	t, err := template.New("pvc").Parse(pvcTemplate)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// parseSelector convertit "k1=v1,k2=v2" en map.
func parseSelector(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return nil, fmt.Errorf("sélecteur invalide: %q (attendu k=v,k=v)", kv)
		}
		out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return out, nil
}
