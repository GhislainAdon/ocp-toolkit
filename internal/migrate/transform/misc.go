package transform

import "fmt"

// transformNamespace enrichit un Namespace avec les annotations projet
// OpenShift. On conserve un Namespace (et pas un ProjectRequest) : c'est
// ce qu'ArgoCD sait réconcilier, et OpenShift le décore en Project.
func transformNamespace(doc map[string]any) Result {
	res := Result{}
	name := Name(doc)
	meta := ensureMap(doc, "metadata")
	ann := ensureMap(meta, "annotations")
	if _, ok := ann["openshift.io/display-name"]; !ok {
		ann["openshift.io/display-name"] = name
	}
	if _, ok := ann["openshift.io/description"]; !ok {
		ann["openshift.io/description"] = "Migré depuis Kubernetes vanilla par ocp-toolkit"
	}
	res.Actions = append(res.Actions, fmt.Sprintf(
		"Namespace %s : annotations projet OpenShift ajoutées (display-name, description)", name))
	res.Warnings = append(res.Warnings, fmt.Sprintf(
		"Namespace %s : vérifier quotas/limites (ClusterResourceQuota) et NetworkPolicy par défaut du cluster cible", name))
	res.Docs = []map[string]any{annotateMigrated(doc)}
	return res
}

// transformService signale (ou corrige) les Service NodePort : sur
// OpenShift l'exposition externe passe par une Route/Ingress, pas par
// l'ouverture d'un port sur chaque nœud.
func transformService(doc map[string]any, o Options) Result {
	res := Result{}
	name := Name(doc)
	spec := getMap(doc, "spec")
	if spec != nil && str(spec["type"]) == "NodePort" {
		if o.FixNodePort {
			spec["type"] = "ClusterIP"
			for _, p := range getSlice(spec, "ports") {
				if pm, ok := p.(map[string]any); ok {
					delete(pm, "nodePort")
				}
			}
			delete(spec, "externalTrafficPolicy")
			res.Actions = append(res.Actions, fmt.Sprintf(
				"Service %s : NodePort → ClusterIP (exposer via une Route si accès externe requis)", name))
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"Service %s : type NodePort — anti-pattern sur OpenShift. Préférer ClusterIP + Route (--fix-nodeport pour convertir)", name))
		}
	}
	res.Docs = []map[string]any{annotateMigrated(doc)}
	return res
}

// transformPVC applique le mapping de StorageClass configuré.
func transformPVC(doc map[string]any, o Options) Result {
	res := Result{}
	name := Name(doc)
	spec := getMap(doc, "spec")
	if spec == nil {
		res.Docs = []map[string]any{annotateMigrated(doc)}
		return res
	}
	sc := str(spec["storageClassName"])
	if mapped, ok := o.StorageClassMap[sc]; ok && sc != "" {
		spec["storageClassName"] = mapped
		res.Actions = append(res.Actions, fmt.Sprintf(
			"PVC %s : StorageClass %s → %s", name, sc, mapped))
	} else if sc == "" {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"PVC %s : pas de storageClassName — vérifier la StorageClass par défaut du cluster OpenShift cible", name))
	} else {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"PVC %s : StorageClass %q non mappée (--storage-class-map %s=<cible>) — vérifier son existence sur la cible", name, sc, sc))
	}
	res.Warnings = append(res.Warnings, fmt.Sprintf(
		"PVC %s : les DONNÉES ne sont pas migrées par cet outil — utiliser MTC/Crane ou une réplication stockage", name))
	res.Docs = []map[string]any{annotateMigrated(doc)}
	return res
}
