package transform

import "fmt"

// transformIngress convertit un Ingress en une ou plusieurs Route OpenShift.
// Chaque couple (host, path) devient une Route dédiée — modèle natif OpenShift.
//
// Note : OpenShift embarque aussi un contrôleur ingress-to-route qui fait
// cette conversion à la volée ; la conversion statique produite ici donne des
// manifestes GitOps de première classe (diff ArgoCD lisibles, TLS explicite).
func transformIngress(doc map[string]any) Result {
	res := Result{}
	name := Name(doc)
	ns := Namespace(doc)

	// Hosts couverts par une section TLS → Route en terminaison edge.
	tlsHosts := map[string]string{} // host → secretName
	for _, t := range getSlice(doc, "spec", "tls") {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		secret := str(tm["secretName"])
		for _, h := range getSlice(tm, "hosts") {
			tlsHosts[str(h)] = secret
		}
	}

	if getString(doc, "spec", "ingressClassName") != "" {
		res.Actions = append(res.Actions, fmt.Sprintf(
			"Ingress %s : ingressClassName supprimé (le routeur OpenShift prend le relais)", name))
	}
	if getMap(doc, "spec", "defaultBackend") != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"Ingress %s : defaultBackend sans règle host/path — pas d'équivalent Route direct, à traiter manuellement", name))
	}

	rules := getSlice(doc, "spec", "rules")
	total := 0
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		if rm == nil {
			continue
		}
		total += len(getSlice(rm, "http", "paths"))
	}

	idx := 0
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		if rm == nil {
			continue
		}
		host := str(rm["host"])
		for _, p := range getSlice(rm, "http", "paths") {
			pm, _ := p.(map[string]any)
			if pm == nil {
				continue
			}
			routeName := name
			if total > 1 {
				routeName = fmt.Sprintf("%s-%d", name, idx)
			}
			idx++

			svcName := getString(pm, "backend", "service", "name")
			if svcName == "" {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"Ingress %s : backend sans service nommé (backend par resource ?) — ignoré", name))
				continue
			}

			spec := map[string]any{
				"to": map[string]any{"kind": "Service", "name": svcName, "weight": 100},
			}
			if host != "" {
				spec["host"] = host
			}
			if path := str(pm["path"]); path != "" && path != "/" {
				spec["path"] = path
			}
			if pt := str(pm["pathType"]); pt == "ImplementationSpecific" {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"Ingress %s : pathType ImplementationSpecific sur %q — vérifier la sémantique côté Route", name, str(pm["path"])))
			}
			port := getMap(pm, "backend", "service", "port")
			if port != nil {
				var target any
				if n, ok := port["name"]; ok && str(n) != "" {
					target = n
				} else if num, ok := port["number"]; ok {
					target = num
				}
				if target != nil {
					spec["port"] = map[string]any{"targetPort": target}
				}
			}
			if secret, ok := tlsHosts[host]; ok {
				spec["tls"] = map[string]any{
					"termination":                   "edge",
					"insecureEdgeTerminationPolicy": "Redirect",
				}
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"Route %s : TLS edge activé mais le certificat du Secret %q n'est PAS transféré — "+
						"utiliser le certificat wildcard du routeur, cert-manager, ou coller cert/clé dans spec.tls", routeName, secret))
			}

			route := map[string]any{
				"apiVersion": "route.openshift.io/v1",
				"kind":       "Route",
				"metadata": map[string]any{
					"name":        routeName,
					"annotations": map[string]any{"ocp-toolkit/migrated-from": "Ingress/" + name},
				},
				"spec": spec,
			}
			if ns != "" {
				route["metadata"].(map[string]any)["namespace"] = ns
			}
			res.Docs = append(res.Docs, route)
		}
	}

	res.Actions = append(res.Actions, fmt.Sprintf(
		"Ingress %s converti en %d Route(s) OpenShift", name, len(res.Docs)))
	if len(res.Docs) == 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"Ingress %s : aucune Route générée (pas de règles http exploitables)", name))
	}
	return res
}
