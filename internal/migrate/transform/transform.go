// Package transform — transformation ressource par ressource des manifestes
// Kubernetes vanilla vers OpenShift. Chaque transformer est une fonction
// pure (map in → maps out + warnings + actions) testée unitairement.
//
// Positionnement : cet outil ne migre PAS les données (volumes, état) —
// c'est le rôle de MTC/Crane côté Red Hat. Ici : manifestes propres,
// prêts pour GitOps.
package transform

import (
	"fmt"
	"strings"
)

// Options de transformation, injectées depuis la CLI.
type Options struct {
	// ImageRewrites : préfixe source → préfixe cible
	// (ex. "docker.io" → "registry.apps.prod.example.com/mirror").
	ImageRewrites map[string]string
	// StorageClassMap : StorageClass source → cible (ex. gp2 → ocs-…-rbd).
	StorageClassMap map[string]string
	// FixSCC supprime les runAsUser/runAsGroup/fsGroup codés en dur pour
	// laisser restricted-v2 assigner les UID (sinon simple WARN).
	FixSCC bool
	// FixNodePort convertit les Service NodePort en ClusterIP.
	FixNodePort bool
}

// Result d'une transformation : documents produits, actions appliquées
// (traçabilité), warnings nécessitant une revue humaine.
type Result struct {
	Docs     []map[string]any
	Actions  []string
	Warnings []string
}

// Transform dispatche un document YAML décodé vers son transformer.
func Transform(doc map[string]any, o Options) Result {
	switch strings.ToLower(Kind(doc)) {
	case "ingress":
		return transformIngress(doc)
	case "namespace":
		return transformNamespace(doc)
	case "deployment", "statefulset", "daemonset":
		return transformWorkload(doc, o)
	case "service":
		return transformService(doc, o)
	case "persistentvolumeclaim":
		return transformPVC(doc, o)
	case "configmap":
		return Result{Docs: []map[string]any{annotateMigrated(doc)}}
	case "secret":
		return Result{
			Docs: []map[string]any{annotateMigrated(doc)},
			Warnings: []string{fmt.Sprintf(
				"Secret %s copié tel quel : re-chiffrer pour GitOps (SealedSecrets / External Secrets / SOPS) avant commit", Name(doc))},
		}
	default:
		return Result{
			Docs: []map[string]any{annotateMigrated(doc)},
			Warnings: []string{fmt.Sprintf(
				"type %s non géré par les transformers : copié tel quel, revue manuelle conseillée", Kind(doc))},
		}
	}
}

// --- helpers d'accès générique (yaml.v3 décode en map[string]any) ---

// Kind retourne le kind du document ("" si absent).
func Kind(doc map[string]any) string { return str(doc["kind"]) }

// Name retourne metadata.name.
func Name(doc map[string]any) string { return getString(doc, "metadata", "name") }

// Namespace retourne metadata.namespace.
func Namespace(doc map[string]any) string { return getString(doc, "metadata", "namespace") }

func str(v any) string {
	s, _ := v.(string)
	return s
}

func getMap(m map[string]any, keys ...string) map[string]any {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func getSlice(m map[string]any, keys ...string) []any {
	if len(keys) == 0 {
		return nil
	}
	parent := m
	if len(keys) > 1 {
		parent = getMap(m, keys[:len(keys)-1]...)
	}
	if parent == nil {
		return nil
	}
	s, _ := parent[keys[len(keys)-1]].([]any)
	return s
}

func getString(m map[string]any, keys ...string) string {
	if len(keys) == 0 {
		return ""
	}
	parent := m
	if len(keys) > 1 {
		parent = getMap(m, keys[:len(keys)-1]...)
	}
	if parent == nil {
		return ""
	}
	return str(parent[keys[len(keys)-1]])
}

// ensureMap retourne m[k] en le créant si besoin.
func ensureMap(m map[string]any, k string) map[string]any {
	if next, ok := m[k].(map[string]any); ok {
		return next
	}
	next := map[string]any{}
	m[k] = next
	return next
}

// annotateMigrated trace le passage de l'outil sans toucher aux labels
// (les labels restent la propriété de l'application / d'ArgoCD).
func annotateMigrated(doc map[string]any) map[string]any {
	meta := ensureMap(doc, "metadata")
	ann := ensureMap(meta, "annotations")
	ann["ocp-toolkit/migrated"] = "true"
	return doc
}

// CleanExported purge les champs runtime d'une ressource exportée d'un
// cluster (get -o json) pour produire un manifeste GitOps propre.
func CleanExported(doc map[string]any) map[string]any {
	delete(doc, "status")
	if meta := getMap(doc, "metadata"); meta != nil {
		for _, k := range []string{
			"managedFields", "uid", "resourceVersion", "generation",
			"creationTimestamp", "selfLink", "ownerReferences",
		} {
			delete(meta, k)
		}
		if ann, ok := meta["annotations"].(map[string]any); ok {
			for _, k := range []string{
				"kubectl.kubernetes.io/last-applied-configuration",
				"deployment.kubernetes.io/revision",
			} {
				delete(ann, k)
			}
			if len(ann) == 0 {
				delete(meta, "annotations")
			}
		}
	}
	// Champs assignés par le contrôleur sur les Services.
	if strings.EqualFold(Kind(doc), "service") {
		if spec := getMap(doc, "spec"); spec != nil {
			for _, k := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy", "internalTrafficPolicy", "sessionAffinityConfig"} {
				delete(spec, k)
			}
		}
	}
	if strings.EqualFold(Kind(doc), "persistentvolumeclaim") {
		if spec := getMap(doc, "spec"); spec != nil {
			delete(spec, "volumeName")
		}
		if meta := getMap(doc, "metadata"); meta != nil {
			if ann, ok := meta["annotations"].(map[string]any); ok {
				for k := range ann {
					if strings.HasPrefix(k, "pv.kubernetes.io/") || strings.HasPrefix(k, "volume.") {
						delete(ann, k)
					}
				}
			}
		}
	}
	return doc
}
