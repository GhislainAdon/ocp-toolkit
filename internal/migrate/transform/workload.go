package transform

import (
	"fmt"
	"strings"
)

// transformWorkload traite Deployment / StatefulSet / DaemonSet :
// compatibilité SCC restricted-v2 (piège n°1 des migrations K8s → OpenShift)
// et réécriture optionnelle des images vers un registre miroir/interne.
func transformWorkload(doc map[string]any, o Options) Result {
	res := Result{}
	ref := Kind(doc) + "/" + Name(doc)
	podSpec := getMap(doc, "spec", "template", "spec")
	if podSpec == nil {
		res.Warnings = append(res.Warnings, ref+" : spec.template.spec introuvable — document copié tel quel")
		res.Docs = []map[string]any{annotateMigrated(doc)}
		return res
	}

	// --- securityContext au niveau pod ---
	if sc := getMap(podSpec, "securityContext"); sc != nil {
		for _, field := range []string{"runAsUser", "runAsGroup", "fsGroup"} {
			if _, present := sc[field]; !present {
				continue
			}
			if o.FixSCC {
				delete(sc, field)
				res.Actions = append(res.Actions, fmt.Sprintf(
					"%s : %s supprimé du securityContext pod (restricted-v2 assigne les UID/GID du namespace)", ref, field))
			} else {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"%s : %s codé en dur — incompatible restricted-v2 (UID hors plage du namespace). --fix-scc pour corriger", ref, field))
			}
		}
		if len(sc) == 0 {
			delete(podSpec, "securityContext")
		}
	}

	// --- hostNetwork / hostPID / hostIPC : jamais auto-corrigés ---
	for _, field := range []string{"hostNetwork", "hostPID", "hostIPC"} {
		if v, ok := podSpec[field].(bool); ok && v {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s : %s=true — interdit par restricted-v2. Nécessite une SCC dédiée (hostnetwork-v2) ou une refonte", ref, field))
		}
	}

	// --- volumes hostPath ---
	for _, v := range getSlice(podSpec, "volumes") {
		vm, _ := v.(map[string]any)
		if vm == nil {
			continue
		}
		if hp := getMap(vm, "hostPath"); hp != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s : volume hostPath %q (%s) — interdit par restricted-v2. Migrer vers PVC/ConfigMap ou SCC dédiée",
				ref, str(vm["name"]), str(hp["path"])))
		}
	}

	// --- conteneurs (init + main) ---
	for _, listKey := range []string{"initContainers", "containers"} {
		for _, c := range getSlice(podSpec, listKey) {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			cname := str(cm["name"])

			if sc := getMap(cm, "securityContext"); sc != nil {
				if v, ok := sc["privileged"].(bool); ok && v {
					res.Warnings = append(res.Warnings, fmt.Sprintf(
						"%s conteneur %s : privileged=true — interdit par restricted-v2, jamais auto-corrigé. "+
							"SCC privileged à réserver aux cas système justifiés", ref, cname))
				}
				if adds := getSlice(sc, "capabilities", "add"); len(adds) > 0 {
					var caps []string
					for _, a := range adds {
						caps = append(caps, str(a))
					}
					res.Warnings = append(res.Warnings, fmt.Sprintf(
						"%s conteneur %s : capabilities ajoutées [%s] — restricted-v2 n'autorise que NET_BIND_SERVICE",
						ref, cname, strings.Join(caps, ", ")))
				}
				if _, present := sc["runAsUser"]; present {
					if o.FixSCC {
						delete(sc, "runAsUser")
						res.Actions = append(res.Actions, fmt.Sprintf(
							"%s conteneur %s : runAsUser supprimé (assigné par restricted-v2)", ref, cname))
						if len(sc) == 0 {
							delete(cm, "securityContext")
						}
					} else {
						res.Warnings = append(res.Warnings, fmt.Sprintf(
							"%s conteneur %s : runAsUser codé en dur — incompatible restricted-v2. --fix-scc pour corriger", ref, cname))
					}
				}
			}

			// Réécriture d'image vers registre miroir/interne.
			if img := str(cm["image"]); img != "" {
				for src, dst := range o.ImageRewrites {
					if strings.HasPrefix(img, src) {
						newImg := strings.TrimSuffix(dst, "/") + strings.TrimPrefix(img, src)
						cm["image"] = newImg
						res.Actions = append(res.Actions, fmt.Sprintf(
							"%s conteneur %s : image %s → %s", ref, cname, img, newImg))
						break
					}
				}
			}
		}
	}

	res.Docs = []map[string]any{annotateMigrated(doc)}
	return res
}
