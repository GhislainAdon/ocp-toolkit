// Package validate — MODULE 1 : validation des nœuds migrés vers OpenShift.
//
// Architecture : les checks « plan de contrôle » (Ready, pressions, CRI-O,
// MachineConfig, labels/taints) sortent d'UN appel API. Les checks « dans le
// nœud » (DNS, pod-à-pod, registre, egress, SELinux, SCC) tournent dans un
// DaemonSet de smoke-test — un pod par nœud, collecte parallèle des logs.
// Le stockage CSI est testé par échantillonnage (--pvc-sample) pour ne pas
// noyer le backend avec 5000 PVC simultanés.
package validate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/GhislainAdon/ocp-toolkit/internal/common/kube"
	"github.com/GhislainAdon/ocp-toolkit/internal/common/logging"
	"github.com/GhislainAdon/ocp-toolkit/internal/common/report"
	"github.com/GhislainAdon/ocp-toolkit/internal/common/ui"
)

// Options du module validate.
type Options struct {
	Kube         *kube.Client
	Log          *logging.Logger
	ReportsDir   string
	ManifestsDir string
	Confirm      bool

	Namespace       string
	NodeSelector    string
	Image           string
	RegistryMirror  string
	EgressTarget    string
	PVCStorageClass string
	PVCSample       int
	Parallel        int
	TimeoutMin      int
	TolerateAll     bool
	Keep            bool
	OPCT            bool
}

type podResult struct {
	Node   string         `json:"node"`
	Checks []report.Check `json:"checks"`
}

const defaultImage = "registry.access.redhat.com/ubi9/ubi-minimal:latest"

// Run exécute le module 1 de bout en bout.
func Run(o Options) error {
	if o.Namespace == "" {
		o.Namespace = "ocptk-validate"
	}
	if o.Parallel <= 0 {
		o.Parallel = 30
	}
	if o.TimeoutMin <= 0 {
		o.TimeoutMin = 15
	}
	if o.PVCSample <= 0 {
		o.PVCSample = 5
	}
	image := o.Image
	if image == "" {
		image = defaultImage
		if o.RegistryMirror != "" {
			// registry.access.redhat.com/ubi9/... → <mirror>/ubi9/...
			image = strings.TrimSuffix(o.RegistryMirror, "/") + "/ubi9/ubi-minimal:latest"
		}
	}

	ui.Title("Module 1 — Validation des nœuds migrés")
	if !o.Confirm {
		ui.Warn("mode DRY-RUN : checks API en lecture seule + validation des manifestes. Exécution réelle : --confirm")
	}

	rep := report.New("validate", o.Kube.Context)
	rep.DryRun = !o.Confirm

	// --- 1. Checks côté API (toujours, lecture seule) ---
	ui.Info("Inspection des nœuds via l'API…")
	nodeChecks, order, err := apiNodeChecks(o.Kube, o.NodeSelector, rep)
	switch {
	case err == nil:
		ui.Success("%d nœud(s) inspecté(s) via l'API", len(order))
	case strings.Contains(err.Error(), "Forbidden"):
		// RBAC restreint (opérateur sans cluster-reader) : on dégrade au lieu
		// d'échouer — les smoke-tests par pod restent possibles.
		ui.Warn("lecture des nœuds interdite (RBAC) : checks API par nœud ignorés — donner 'cluster-reader' à l'opérateur pour la vue complète")
		rep.Notes = append(rep.Notes,
			"Nœuds non listables (RBAC restreint) : checks API par nœud absents ; "+
				"résultats limités aux smoke-tests des pods. Rôle 'cluster-reader' recommandé.")
		nodeChecks, order = map[string][]report.Check{}, nil
	default:
		return fmt.Errorf("lecture des nœuds: %w", err)
	}
	o.Log.Info("validate: %d noeuds, selector=%q", len(order), o.NodeSelector)

	// --- 2. Rendu du DaemonSet de smoke-test ---
	nodeSel, err := parseSelector(o.NodeSelector)
	if err != nil {
		return err
	}
	manifest, err := renderManifests(dsParams{
		Namespace:    o.Namespace,
		Image:        image,
		DNSPodIPs:    dnsPodIPs(o.Kube),
		EgressTarget: o.EgressTarget,
		TolerateAll:  o.TolerateAll,
		NodeSelector: nodeSel,
	})
	if err != nil {
		return fmt.Errorf("rendu des manifestes: %w", err)
	}

	// Namespace déjà existant (ex. --namespace <projet> sur un cluster où l'on
	// ne peut pas créer de namespace) : on le réutilise sans jamais le posséder
	// — ni création, ni suppression au nettoyage.
	nsExists := false
	if _, err := o.Kube.Run(nil, "get", "namespace", o.Namespace); err == nil {
		nsExists = true
		if i := bytes.Index(manifest, []byte("\n---")); i >= 0 {
			manifest = manifest[i+4:] // retire le document Namespace (1er du template)
		}
		ui.Info("Namespace %s existant : réutilisé (il ne sera ni modifié ni supprimé)", o.Namespace)
	}

	if o.ManifestsDir != "" {
		dir := filepath.Join(o.ManifestsDir, "validate")
		_ = os.MkdirAll(dir, 0o755)
		path := filepath.Join(dir, "smoke-daemonset.yaml")
		if err := os.WriteFile(path, manifest, 0o644); err == nil {
			ui.Info("Manifestes écrits : %s", path)
		}
	}

	if !o.Confirm {
		// Validation côté client uniquement — aucune mutation.
		o.Kube.DryRun = true
		if _, err := o.Kube.Apply(manifest); err != nil {
			ui.Warn("validation des manifestes (--dry-run=client) : %v", err)
			rep.Notes = append(rep.Notes, "Validation dry-run des manifestes en échec : "+err.Error())
		} else {
			ui.Success("manifestes valides (apply --dry-run=client)")
		}
		rep.Notes = append(rep.Notes,
			"DRY-RUN : seuls les checks API (lecture seule) figurent dans ce rapport. "+
				"Relancer avec --confirm pour déployer le DaemonSet de smoke-tests.")
	} else {
		if err := runSmokeTests(o, image, manifest, nodeChecks, order, nsExists); err != nil {
			return err
		}
	}

	// --- 5. Rapport + diff entre vagues ---
	// Union : nœuds vus par l'API + nœuds découverts via les pods de test
	// (cas RBAC restreint où l'API nodes est interdite).
	seen := map[string]bool{}
	for _, n := range order {
		rep.Items = append(rep.Items, report.Item{Name: n, Checks: nodeChecks[n]})
		seen[n] = true
	}
	for n, checks := range nodeChecks {
		if !seen[n] {
			rep.Items = append(rep.Items, report.Item{Name: n, Checks: checks})
		}
	}
	rep.Finalize()
	prev, _ := report.LoadPrevious(o.ReportsDir, "validate")
	rep.Diff = report.ComputeDiff(prev, rep)
	dir, err := rep.Write(o.ReportsDir)
	if err != nil {
		return fmt.Errorf("écriture du rapport: %w", err)
	}

	printSummary(rep, dir)
	if o.OPCT {
		opctGuidance()
	}
	return nil
}

// runSmokeTests déploie le DaemonSet, collecte les résultats et nettoie.
func runSmokeTests(o Options, image string, manifest []byte, nodeChecks map[string][]report.Check, order []string, nsExists bool) error {
	o.Kube.DryRun = false
	ui.Info("Déploiement du DaemonSet de smoke-test (namespace %s)…", o.Namespace)
	if _, err := o.Kube.Apply(manifest); err != nil {
		return fmt.Errorf("apply du DaemonSet: %w", err)
	}
	if !o.Keep {
		defer func() {
			if nsExists {
				// Namespace partagé : on ne retire QUE nos ressources.
				ui.Info("Nettoyage des ressources ocptk dans %s (namespace conservé)…", o.Namespace)
				o.Kube.DeleteIgnore("-n", o.Namespace, "daemonset", "ocptk-validate")
				o.Kube.DeleteIgnore("-n", o.Namespace, "configmap", "ocptk-checks")
				o.Kube.DeleteIgnore("-n", o.Namespace, "serviceaccount", "ocptk-validate")
			} else {
				ui.Info("Nettoyage du namespace %s…", o.Namespace)
				o.Kube.DeleteIgnore("namespace", o.Namespace)
			}
		}()
	}

	// Attente du rollout avec progression.
	deadline := time.Now().Add(time.Duration(o.TimeoutMin) * time.Minute)
	for {
		var ds struct {
			Status struct {
				DesiredNumberScheduled int `json:"desiredNumberScheduled"`
				NumberReady            int `json:"numberReady"`
			} `json:"status"`
		}
		if err := o.Kube.GetJSON(&ds, "get", "daemonset", "ocptk-validate", "-n", o.Namespace); err == nil {
			fmt.Printf("\r  pods de test prêts : %d/%d ", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
			if ds.Status.DesiredNumberScheduled > 0 && ds.Status.NumberReady >= ds.Status.DesiredNumberScheduled {
				fmt.Println()
				break
			}
		}
		if time.Now().After(deadline) {
			fmt.Println()
			ui.Warn("timeout (%d min) : collecte partielle des résultats", o.TimeoutMin)
			break
		}
		time.Sleep(10 * time.Second)
	}

	// Collecte parallèle des logs (pool de workers).
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := o.Kube.GetJSON(&pods, "get", "pods", "-n", o.Namespace, "-l", "app=ocptk-validate"); err != nil {
		return fmt.Errorf("liste des pods de test: %w", err)
	}
	ui.Info("Collecte des résultats de %d pod(s), %d en parallèle…", len(pods.Items), o.Parallel)

	sem := make(chan struct{}, o.Parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[string]bool{}
	for _, p := range pods.Items {
		wg.Add(1)
		go func(podName, node string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res := collectPod(o.Kube, o.Namespace, podName)
			mu.Lock()
			defer mu.Unlock()
			if res != nil && res.Node != "" {
				nodeChecks[res.Node] = append(nodeChecks[res.Node], res.Checks...)
				got[res.Node] = true
			} else if node != "" {
				nodeChecks[node] = append(nodeChecks[node], report.Check{
					Name: "smoke-pod", Status: report.Fail,
					Detail: "résultat OCPTK-RESULT non reçu du pod " + podName,
				})
				got[node] = true
			}
		}(p.Metadata.Name, p.Spec.NodeName)
	}
	wg.Wait()

	// Nœuds sans pod de test (taints non tolérés, NotReady…).
	for _, n := range order {
		if !got[n] {
			nodeChecks[n] = append(nodeChecks[n], report.Check{
				Name: "smoke-pod", Status: report.Warn,
				Detail: "aucun pod de smoke-test programmé sur ce nœud (taint non toléré ? nœud NotReady ? essayer --tolerate-all)",
			})
		}
	}

	// Stockage CSI échantillonné.
	if o.PVCStorageClass != "" {
		sampleNodes := order
		if len(sampleNodes) > o.PVCSample {
			sampleNodes = sampleNodes[:o.PVCSample]
		}
		ui.Info("Test PVC (StorageClass %s) sur %d nœud(s) échantillonné(s)…", o.PVCStorageClass, len(sampleNodes))
		runPVCProbes(o, image, sampleNodes, nodeChecks)
	}
	return nil
}

// collectPod lit les logs d'un pod et extrait la ligne OCPTK-RESULT.
// Deux tentatives : le pod peut être Ready sans que les logs soient flushés.
func collectPod(c *kube.Client, ns, pod string) *podResult {
	for attempt := 0; attempt < 2; attempt++ {
		out, err := c.Run(nil, "logs", "-n", ns, pod)
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				if rest, ok := strings.CutPrefix(line, "OCPTK-RESULT "); ok {
					var res podResult
					if json.Unmarshal([]byte(rest), &res) == nil {
						return &res
					}
				}
			}
		}
		time.Sleep(5 * time.Second)
	}
	return nil
}

// runPVCProbes crée PVC+pod pinné par nœud échantillonné, attend PVC-OK.
func runPVCProbes(o Options, image string, nodes []string, nodeChecks map[string][]report.Check) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, node := range nodes {
		wg.Add(1)
		go func(i int, node string) {
			defer wg.Done()
			name := fmt.Sprintf("ocptk-pvc-probe-%d", i)
			manifest, err := renderPVCProbe(pvcParams{
				Name: name, Namespace: o.Namespace, Node: node,
				Image: image, StorageClass: o.PVCStorageClass,
			})
			check := report.Check{Name: "storage-pvc-mount", Status: report.Fail}
			defer func() {
				o.Kube.DeleteIgnore("-n", o.Namespace, "pod", name)
				o.Kube.DeleteIgnore("-n", o.Namespace, "pvc", name)
				mu.Lock()
				nodeChecks[node] = append(nodeChecks[node], check)
				mu.Unlock()
			}()
			if err != nil {
				check.Detail = "rendu manifeste PVC: " + err.Error()
				return
			}
			if _, err := o.Kube.Apply(manifest); err != nil {
				check.Detail = "apply PVC/pod: " + err.Error()
				return
			}
			deadline := time.Now().Add(3 * time.Minute)
			for time.Now().Before(deadline) {
				out, _ := o.Kube.Run(nil, "logs", "-n", o.Namespace, name)
				if strings.Contains(out, "PVC-OK") {
					check.Status = report.Pass
					check.Detail = fmt.Sprintf("PVC 1Gi (%s) créé, monté et écrit sur %s", o.PVCStorageClass, node)
					return
				}
				time.Sleep(10 * time.Second)
			}
			check.Detail = fmt.Sprintf("PVC non monté en 3 min sur %s (StorageClass %s) — vérifier le provisioner CSI", node, o.PVCStorageClass)
		}(i, node)
	}
	wg.Wait()
}

func printSummary(rep *report.Report, dir string) {
	ui.Title("Synthèse")
	s := rep.Summary
	ui.Info("Nœuds : %s%d PASS%s / %s%d FAIL%s / %s%d WARN%s / %d SKIP (total %d)",
		ui.Green, s.Pass, ui.Reset, ui.Red, s.Fail, ui.Reset, ui.Yellow, s.Warn, ui.Reset, s.Skip, s.Total)

	// Tableau des nœuds en échec (15 max en console, tout est dans le rapport).
	var rows [][]string
	for _, it := range rep.Items {
		if it.Status != report.Fail {
			continue
		}
		for _, c := range it.Checks {
			if c.Status == report.Fail {
				rows = append(rows, []string{it.Name, c.Name, c.Detail})
			}
		}
		if len(rows) >= 15 {
			break
		}
	}
	if len(rows) > 0 {
		fmt.Println()
		ui.Table([]string{"NŒUD", "CHECK", "DÉTAIL"}, rows)
	}

	if d := rep.Diff; d != nil {
		fmt.Println()
		ui.Info("Diff vs vague %s : %s%d régression(s)%s, %d nouveau(x) échec(s), %s%d corrigé(s)%s, %d persistant(s)",
			d.Previous, ui.Red, len(d.Regressed), ui.Reset, len(d.NewFail), ui.Green, len(d.Fixed), ui.Reset, len(d.Persistent))
	}
	fmt.Println()
	ui.Success("Rapport : %s (report.json, report.html, junit.xml)", dir)
}

func opctGuidance() {
	ui.Title("Conformance cluster (OPCT)")
	if _, err := exec.LookPath("opct"); err == nil {
		ui.Info("Binaire opct détecté. Lancement type :")
		ui.Info("  opct run --watch        # ~1-3h, cluster dédié recommandé")
		ui.Info("  opct retrieve && opct report <archive>")
	} else {
		ui.Info("Binaire opct non trouvé. Téléchargement :")
		ui.Info("  https://github.com/redhat-openshift-ecosystem/provider-certification-tool/releases")
	}
	ui.Info("OPCT (Sonobuoy + openshift-tests) valide la conformance GLOBALE du cluster,")
	ui.Info("en complément des smoke-tests par nœud de ce module.")
}
