package validate

import (
	"fmt"
	"strings"

	"github.com/GhislainAdon/ocp-toolkit/internal/common/kube"
	"github.com/GhislainAdon/ocp-toolkit/internal/common/report"
)

// Checks côté API (lecture seule, exécutés même en dry-run) :
// état du nœud, pressions ressources, runtime CRI-O, état MachineConfig,
// labels/taints. À 5000 nœuds c'est UN seul appel API (get nodes -o json).

type nodeJSON struct {
	Items []struct {
		Metadata struct {
			Name        string            `json:"name"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Unschedulable bool `json:"unschedulable"`
			Taints        []struct {
				Key    string `json:"key"`
				Effect string `json:"effect"`
			} `json:"taints"`
		} `json:"spec"`
		Status struct {
			Conditions []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"conditions"`
			NodeInfo struct {
				KubeletVersion          string `json:"kubeletVersion"`
				ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
				OSImage                 string `json:"osImage"`
			} `json:"nodeInfo"`
		} `json:"status"`
	} `json:"items"`
}

// apiNodeChecks retourne les checks par nœud + l'ordre des nœuds.
func apiNodeChecks(c *kube.Client, selector string, rep *report.Report) (map[string][]report.Check, []string, error) {
	var nodes nodeJSON
	args := []string{"get", "nodes"}
	if selector != "" {
		args = append(args, "-l", selector)
	}
	if err := c.GetJSON(&nodes, args...); err != nil {
		return nil, nil, err
	}

	checks := make(map[string][]report.Check, len(nodes.Items))
	order := make([]string, 0, len(nodes.Items))

	for _, n := range nodes.Items {
		name := n.Metadata.Name
		order = append(order, name)
		var cs []report.Check

		// 1a. Nœud Ready + kubelet
		ready, readyMsg := "Unknown", ""
		pressures := []string{}
		for _, cond := range n.Status.Conditions {
			switch cond.Type {
			case "Ready":
				ready, readyMsg = cond.Status, cond.Message
			case "MemoryPressure", "DiskPressure", "PIDPressure":
				if cond.Status == "True" {
					pressures = append(pressures, cond.Type)
				}
			}
		}
		if ready == "True" {
			cs = append(cs, report.Check{Name: "node-ready", Status: report.Pass,
				Detail: fmt.Sprintf("Ready — kubelet %s", n.Status.NodeInfo.KubeletVersion)})
		} else {
			cs = append(cs, report.Check{Name: "node-ready", Status: report.Fail,
				Detail: fmt.Sprintf("Ready=%s — %s", ready, readyMsg)})
		}

		// 1b. Pressions ressources
		if len(pressures) == 0 {
			cs = append(cs, report.Check{Name: "node-pressure", Status: report.Pass,
				Detail: "MemoryPressure/DiskPressure/PIDPressure = False"})
		} else {
			cs = append(cs, report.Check{Name: "node-pressure", Status: report.Fail,
				Detail: "pression détectée: " + strings.Join(pressures, ", ")})
		}

		// 1c. Runtime CRI-O
		rt := n.Status.NodeInfo.ContainerRuntimeVersion
		if strings.HasPrefix(rt, "cri-o://") {
			cs = append(cs, report.Check{Name: "runtime-crio", Status: report.Pass, Detail: rt})
		} else {
			cs = append(cs, report.Check{Name: "runtime-crio", Status: report.Warn,
				Detail: fmt.Sprintf("runtime %q (attendu cri-o — nœud pas encore migré ?)", rt)})
		}

		// 6. MachineConfig : annotations posées par le MCO sur chaque nœud
		ann := n.Metadata.Annotations
		mcoState := ann["machineconfiguration.openshift.io/state"]
		desired := ann["machineconfiguration.openshift.io/desiredConfig"]
		current := ann["machineconfiguration.openshift.io/currentConfig"]
		switch {
		case mcoState == "":
			cs = append(cs, report.Check{Name: "machineconfig", Status: report.Warn,
				Detail: "annotations MCO absentes (Machine Config Operator pas encore actif sur ce nœud ?)"})
		case mcoState == "Degraded":
			cs = append(cs, report.Check{Name: "machineconfig", Status: report.Fail,
				Detail: "état MCO: Degraded — " + ann["machineconfiguration.openshift.io/reason"]})
		case mcoState == "Done" && desired == current:
			cs = append(cs, report.Check{Name: "machineconfig", Status: report.Pass,
				Detail: "MachineConfig à jour (" + current + ")"})
		default:
			cs = append(cs, report.Check{Name: "machineconfig", Status: report.Warn,
				Detail: fmt.Sprintf("état MCO: %s (desired=%s current=%s) — mise à jour en cours", mcoState, desired, current)})
		}

		// 7. Labels & taints conformes au rôle
		roles := []string{}
		for k := range n.Metadata.Labels {
			if strings.HasPrefix(k, "node-role.kubernetes.io/") {
				roles = append(roles, strings.TrimPrefix(k, "node-role.kubernetes.io/"))
			}
		}
		var taintInfo []string
		for _, t := range n.Spec.Taints {
			taintInfo = append(taintInfo, t.Key+":"+t.Effect)
		}
		switch {
		case len(roles) == 0:
			cs = append(cs, report.Check{Name: "labels-taints", Status: report.Warn,
				Detail: "aucun label node-role.kubernetes.io/* — rôle du nœud non défini"})
		case n.Spec.Unschedulable:
			cs = append(cs, report.Check{Name: "labels-taints", Status: report.Warn,
				Detail: fmt.Sprintf("nœud cordonné (unschedulable) — rôles: %s", strings.Join(roles, ","))})
		default:
			detail := "rôles: " + strings.Join(roles, ",")
			if len(taintInfo) > 0 {
				detail += " — taints: " + strings.Join(taintInfo, ",")
			}
			cs = append(cs, report.Check{Name: "labels-taints", Status: report.Pass, Detail: detail})
		}

		checks[name] = cs
	}

	// État global des MachineConfigPools → notes du rapport.
	mcpNotes(c, rep)
	return checks, order, nil
}

func mcpNotes(c *kube.Client, rep *report.Report) {
	var mcps struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				MachineCount         int `json:"machineCount"`
				UpdatedMachineCount  int `json:"updatedMachineCount"`
				DegradedMachineCount int `json:"degradedMachineCount"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := c.GetJSON(&mcps, "get", "machineconfigpools"); err != nil {
		rep.Notes = append(rep.Notes,
			"MachineConfigPools illisibles (CRD absente ? cluster non-OpenShift ou droits insuffisants): "+err.Error())
		return
	}
	for _, p := range mcps.Items {
		st := p.Status
		note := fmt.Sprintf("MCP %s : %d/%d machines à jour", p.Metadata.Name, st.UpdatedMachineCount, st.MachineCount)
		if st.DegradedMachineCount > 0 {
			note += fmt.Sprintf(" — %d DEGRADÉES", st.DegradedMachineCount)
		}
		rep.Notes = append(rep.Notes, note)
	}
}

// dnsPodIPs récupère quelques IP de pods CoreDNS (openshift-dns) pour le
// test pod-à-pod inter-nœuds. Best-effort : liste vide = check SKIP.
func dnsPodIPs(c *kube.Client) string {
	var pods struct {
		Items []struct {
			Status struct {
				PodIP string `json:"podIP"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := c.GetJSON(&pods, "get", "pods", "-n", "openshift-dns", "-l", "dns.operator.openshift.io/daemonset-dns=default"); err != nil {
		return ""
	}
	var ips []string
	for _, p := range pods.Items {
		if p.Status.PodIP != "" {
			ips = append(ips, p.Status.PodIP)
		}
		if len(ips) >= 5 {
			break
		}
	}
	return strings.Join(ips, " ")
}
