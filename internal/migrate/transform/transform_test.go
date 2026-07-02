package transform

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func mustParse(t *testing.T, s string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatalf("yaml invalide: %v", err)
	}
	return doc
}

func TestIngressToRouteBasic(t *testing.T) {
	doc := mustParse(t, `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: web
  namespace: shop
spec:
  ingressClassName: nginx
  rules:
  - host: shop.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: web-svc
            port:
              number: 8080
`)
	res := Transform(doc, Options{})
	if len(res.Docs) != 1 {
		t.Fatalf("attendu 1 Route, obtenu %d", len(res.Docs))
	}
	route := res.Docs[0]
	if Kind(route) != "Route" {
		t.Errorf("kind = %q, attendu Route", Kind(route))
	}
	if got := getString(route, "spec", "host"); got != "shop.example.com" {
		t.Errorf("host = %q", got)
	}
	if got := getString(route, "spec", "to", "name"); got != "web-svc" {
		t.Errorf("to.name = %q", got)
	}
	if Namespace(route) != "shop" {
		t.Errorf("namespace = %q", Namespace(route))
	}
	if getMap(route, "spec", "tls") != nil {
		t.Error("pas de TLS attendu sans section tls")
	}
}

func TestIngressToRouteTLSAndMultiPath(t *testing.T) {
	doc := mustParse(t, `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: api
spec:
  tls:
  - hosts: [api.example.com]
    secretName: api-cert
  rules:
  - host: api.example.com
    http:
      paths:
      - path: /v1
        pathType: Prefix
        backend:
          service: {name: api-v1, port: {name: http}}
      - path: /v2
        pathType: Prefix
        backend:
          service: {name: api-v2, port: {number: 9090}}
`)
	res := Transform(doc, Options{})
	if len(res.Docs) != 2 {
		t.Fatalf("attendu 2 Routes, obtenu %d", len(res.Docs))
	}
	tls := getMap(res.Docs[0], "spec", "tls")
	if tls == nil || tls["termination"] != "edge" {
		t.Errorf("TLS edge attendu, obtenu %v", tls)
	}
	if getString(res.Docs[0], "spec", "path") != "/v1" {
		t.Errorf("path = %q", getString(res.Docs[0], "spec", "path"))
	}
	// Le certificat non transféré doit produire un warning.
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "api-cert") {
			found = true
		}
	}
	if !found {
		t.Errorf("warning certificat attendu, warnings = %v", res.Warnings)
	}
}

func TestWorkloadSCCFix(t *testing.T) {
	src := `
apiVersion: apps/v1
kind: Deployment
metadata: {name: legacy, namespace: shop}
spec:
  template:
    spec:
      securityContext:
        runAsUser: 1000
        fsGroup: 2000
      containers:
      - name: main
        image: docker.io/library/nginx:1.25
        securityContext:
          runAsUser: 1000
`
	// Sans fix : warnings seulement, document inchangé.
	res := Transform(mustParse(t, src), Options{})
	if len(res.Warnings) < 3 {
		t.Errorf("attendu >=3 warnings SCC, obtenu %v", res.Warnings)
	}
	if getMap(res.Docs[0], "spec", "template", "spec", "securityContext") == nil {
		t.Error("securityContext ne doit pas être modifié sans --fix-scc")
	}

	// Avec fix : champs supprimés, actions tracées.
	res = Transform(mustParse(t, src), Options{FixSCC: true})
	if sc := getMap(res.Docs[0], "spec", "template", "spec", "securityContext"); sc != nil {
		t.Errorf("securityContext pod devrait être vidé, obtenu %v", sc)
	}
	if len(res.Actions) < 3 {
		t.Errorf("attendu >=3 actions, obtenu %v", res.Actions)
	}
}

func TestWorkloadPrivilegedNeverAutoFixed(t *testing.T) {
	doc := mustParse(t, `
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: sysagent}
spec:
  template:
    spec:
      hostNetwork: true
      containers:
      - name: agent
        image: agent:1
        securityContext:
          privileged: true
          capabilities:
            add: [NET_ADMIN, SYS_TIME]
`)
	res := Transform(doc, Options{FixSCC: true})
	// privileged/hostNetwork/capabilities : jamais corrigés, toujours signalés.
	if v, _ := getMap(res.Docs[0], "spec", "template", "spec")["hostNetwork"].(bool); !v {
		t.Error("hostNetwork ne doit jamais être auto-corrigé")
	}
	want := []string{"privileged", "hostNetwork", "NET_ADMIN"}
	for _, w := range want {
		found := false
		for _, warn := range res.Warnings {
			if strings.Contains(warn, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("warning contenant %q attendu, warnings = %v", w, res.Warnings)
		}
	}
}

func TestImageRewrite(t *testing.T) {
	doc := mustParse(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec:
  template:
    spec:
      initContainers:
      - {name: init, image: docker.io/busybox:1.36}
      containers:
      - {name: main, image: docker.io/library/nginx:1.25}
      - {name: side, image: quay.io/acme/tool:2}
`)
	res := Transform(doc, Options{ImageRewrites: map[string]string{
		"docker.io": "registry.apps.prod.local/mirror",
	}})
	podSpec := getMap(res.Docs[0], "spec", "template", "spec")
	main := getSlice(podSpec, "containers")[0].(map[string]any)
	if img := str(main["image"]); img != "registry.apps.prod.local/mirror/library/nginx:1.25" {
		t.Errorf("image réécrite = %q", img)
	}
	side := getSlice(podSpec, "containers")[1].(map[string]any)
	if img := str(side["image"]); img != "quay.io/acme/tool:2" {
		t.Errorf("image non concernée modifiée: %q", img)
	}
	initC := getSlice(podSpec, "initContainers")[0].(map[string]any)
	if img := str(initC["image"]); !strings.HasPrefix(img, "registry.apps.prod.local/mirror/") {
		t.Errorf("initContainer non réécrit: %q", img)
	}
}

func TestServiceNodePort(t *testing.T) {
	src := `
apiVersion: v1
kind: Service
metadata: {name: web}
spec:
  type: NodePort
  ports:
  - {port: 80, nodePort: 30080}
`
	res := Transform(mustParse(t, src), Options{})
	if len(res.Warnings) == 0 {
		t.Error("warning NodePort attendu")
	}
	res = Transform(mustParse(t, src), Options{FixNodePort: true})
	spec := getMap(res.Docs[0], "spec")
	if spec["type"] != "ClusterIP" {
		t.Errorf("type = %v, attendu ClusterIP", spec["type"])
	}
	p := getSlice(spec, "ports")[0].(map[string]any)
	if _, still := p["nodePort"]; still {
		t.Error("nodePort devrait être supprimé")
	}
}

func TestPVCStorageClassMap(t *testing.T) {
	doc := mustParse(t, `
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data}
spec:
  storageClassName: gp2
  resources: {requests: {storage: 10Gi}}
`)
	res := Transform(doc, Options{StorageClassMap: map[string]string{"gp2": "ocs-storagecluster-ceph-rbd"}})
	if sc := getString(res.Docs[0], "spec", "storageClassName"); sc != "ocs-storagecluster-ceph-rbd" {
		t.Errorf("storageClassName = %q", sc)
	}
}

func TestNamespaceAnnotations(t *testing.T) {
	doc := mustParse(t, `
apiVersion: v1
kind: Namespace
metadata: {name: shop}
`)
	res := Transform(doc, Options{})
	if got := getString(res.Docs[0], "metadata", "annotations", "openshift.io/display-name"); got != "shop" {
		t.Errorf("display-name = %q", got)
	}
}

func TestCleanExported(t *testing.T) {
	doc := mustParse(t, `
apiVersion: v1
kind: Service
metadata:
  name: web
  uid: abc-123
  resourceVersion: "42"
  creationTimestamp: "2026-01-01T00:00:00Z"
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: "{}"
spec:
  clusterIP: 172.30.1.1
  ports: [{port: 80}]
status:
  loadBalancer: {}
`)
	out := CleanExported(doc)
	if _, ok := out["status"]; ok {
		t.Error("status devrait être purgé")
	}
	meta := getMap(out, "metadata")
	for _, k := range []string{"uid", "resourceVersion", "creationTimestamp", "annotations"} {
		if _, ok := meta[k]; ok {
			t.Errorf("metadata.%s devrait être purgé", k)
		}
	}
	if _, ok := getMap(out, "spec")["clusterIP"]; ok {
		t.Error("spec.clusterIP devrait être purgé")
	}
}
