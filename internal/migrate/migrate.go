// Package migrate — MODULE 3 : transformation d'applications K8s vanilla en
// manifestes OpenShift-natifs, organisés par projet/application en structure
// Kustomize consommable par ArgoCD.
package migrate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/GhislainAdon/ocp-toolkit/internal/common/kube"
	"github.com/GhislainAdon/ocp-toolkit/internal/common/logging"
	"github.com/GhislainAdon/ocp-toolkit/internal/common/report"
	"github.com/GhislainAdon/ocp-toolkit/internal/common/ui"
	"github.com/GhislainAdon/ocp-toolkit/internal/migrate/transform"
)

// Options du module migrate.
type Options struct {
	Kube       *kube.Client // requis seulement avec FromCluster
	Log        *logging.Logger
	ReportsDir string
	OutputDir  string // generated/migrate

	SourceDir   string
	FromCluster bool
	Namespaces  []string
	Confirm     bool

	TOpts transform.Options
}

// clusterKinds : types exportés depuis un cluster source.
var clusterKinds = []string{
	"deployment", "statefulset", "daemonset", "service",
	"ingress", "configmap", "secret", "persistentvolumeclaim",
}

type appGroup struct {
	project string
	app     string
	results []transform.Result
	docs    []map[string]any
}

// Run exécute le module 3 de bout en bout.
func Run(o Options) error {
	ui.Title("Module 3 — Migration d'applications K8s → OpenShift")

	docs, err := load(o)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return fmt.Errorf("aucune ressource trouvée (source: %s)", sourceLabel(o))
	}
	ui.Success("%d ressource(s) chargée(s) depuis %s", len(docs), sourceLabel(o))

	// Transformation + regroupement projet/application.
	groups := map[string]*appGroup{}
	nsDocs := map[string]map[string]any{} // Namespace transformés, par projet
	rep := report.New("migrate", "")
	rep.DryRun = !o.Confirm

	for _, doc := range docs {
		res := transform.Transform(doc, o.TOpts)
		if strings.EqualFold(transform.Kind(doc), "namespace") {
			if len(res.Docs) > 0 {
				nsDocs[transform.Name(doc)] = res.Docs[0]
			}
			continue
		}
		project := transform.Namespace(doc)
		if project == "" {
			project = "default"
		}
		app := appName(doc)
		key := project + "/" + app
		g, ok := groups[key]
		if !ok {
			g = &appGroup{project: project, app: app}
			groups[key] = g
		}
		g.results = append(g.results, res)
		g.docs = append(g.docs, res.Docs...)
	}

	if !o.Confirm {
		ui.Warn("mode DRY-RUN : rien n'est écrit dans %s. Synthèse ci-dessous ; --confirm pour générer les fichiers.", o.OutputDir)
	}

	// Écriture Kustomize + rapport par application.
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	projects := map[string][]string{} // project → apps
	for _, key := range keys {
		g := groups[key]
		projects[g.project] = append(projects[g.project], g.app)

		item := report.Item{Name: key}
		for _, res := range g.results {
			for _, a := range res.Actions {
				item.Checks = append(item.Checks, report.Check{Name: "action", Status: report.Pass, Detail: a})
			}
			for _, w := range res.Warnings {
				item.Checks = append(item.Checks, report.Check{Name: "revue-requise", Status: report.Warn, Detail: w})
			}
		}
		if len(item.Checks) == 0 {
			item.Checks = append(item.Checks, report.Check{Name: "action", Status: report.Pass,
				Detail: fmt.Sprintf("%d ressource(s) copiée(s) sans transformation nécessaire", len(g.docs))})
		}
		rep.Items = append(rep.Items, item)

		if o.Confirm {
			if err := writeApp(o.OutputDir, g); err != nil {
				return fmt.Errorf("écriture %s: %w", key, err)
			}
		}
	}

	if o.Confirm {
		if err := writeProjects(o.OutputDir, projects, nsDocs); err != nil {
			return err
		}
		ui.Success("Arborescence Kustomize générée dans %s", o.OutputDir)
	}

	rep.Finalize()
	prev, _ := report.LoadPrevious(o.ReportsDir, "migrate")
	rep.Diff = report.ComputeDiff(prev, rep)
	dir, err := rep.Write(o.ReportsDir)
	if err != nil {
		return fmt.Errorf("écriture du rapport: %w", err)
	}

	printSummary(rep, dir)
	return nil
}

func sourceLabel(o Options) string {
	if o.FromCluster {
		return "cluster (namespaces: " + strings.Join(o.Namespaces, ", ") + ")"
	}
	return o.SourceDir
}

// load lit les ressources depuis un dossier YAML ou depuis le cluster source.
func load(o Options) ([]map[string]any, error) {
	if o.FromCluster {
		return loadFromCluster(o)
	}
	if o.SourceDir == "" {
		return nil, fmt.Errorf("préciser --source-dir <dossier> ou --from-cluster --namespaces ns1,ns2")
	}
	var docs []map[string]any
	err := filepath.WalkDir(o.SourceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml")) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				break // fin de fichier ou document invalide : on passe au suivant
			}
			if len(doc) > 0 && transform.Kind(doc) != "" {
				docs = append(docs, doc)
			}
		}
		return nil
	})
	return docs, err
}

func loadFromCluster(o Options) ([]map[string]any, error) {
	if o.Kube == nil {
		return nil, fmt.Errorf("client kube indisponible")
	}
	if len(o.Namespaces) == 0 {
		return nil, fmt.Errorf("--from-cluster requiert --namespaces ns1,ns2")
	}
	var docs []map[string]any
	for _, ns := range o.Namespaces {
		// Le Namespace lui-même.
		var nsDoc map[string]any
		if err := o.Kube.GetJSON(&nsDoc, "get", "namespace", ns); err == nil {
			docs = append(docs, transform.CleanExported(nsDoc))
		}
		for _, kind := range clusterKinds {
			var list struct {
				Items []map[string]any `json:"items"`
			}
			if err := o.Kube.GetJSON(&list, "get", kind, "-n", ns); err != nil {
				ui.Warn("lecture %s dans %s : %v", kind, ns, err)
				continue
			}
			for _, item := range list.Items {
				docs = append(docs, transform.CleanExported(item))
			}
		}
	}
	return docs, nil
}

// appName déduit l'application d'une ressource via ses labels standards.
func appName(doc map[string]any) string {
	meta, _ := doc["metadata"].(map[string]any)
	if meta != nil {
		if labels, ok := meta["labels"].(map[string]any); ok {
			for _, k := range []string{"app.kubernetes.io/name", "app.kubernetes.io/part-of", "app", "k8s-app"} {
				if v, ok := labels[k].(string); ok && v != "" {
					return sanitize(v)
				}
			}
		}
	}
	switch strings.ToLower(transform.Kind(doc)) {
	case "deployment", "statefulset", "daemonset":
		return sanitize(transform.Name(doc))
	}
	return "shared"
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9.-]`)

// marshalYAML sérialise avec l'indentation 2 espaces conventionnelle K8s.
func marshalYAML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

func sanitize(s string) string {
	s = unsafeChars.ReplaceAllString(strings.ToLower(s), "-")
	if s == "" {
		return "app"
	}
	return s
}

// writeApp écrit generated/migrate/<project>/<app>/base/ + README.
func writeApp(root string, g *appGroup) error {
	base := filepath.Join(root, g.project, g.app, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}

	var resources []string
	seen := map[string]int{}
	for _, doc := range g.docs {
		fname := fmt.Sprintf("%s-%s.yaml", strings.ToLower(transform.Kind(doc)), sanitize(transform.Name(doc)))
		if n := seen[fname]; n > 0 {
			fname = fmt.Sprintf("%s-%s-%d.yaml", strings.ToLower(transform.Kind(doc)), sanitize(transform.Name(doc)), n)
		}
		seen[fmt.Sprintf("%s-%s.yaml", strings.ToLower(transform.Kind(doc)), sanitize(transform.Name(doc)))]++
		b, err := marshalYAML(doc)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(base, fname), b, 0o644); err != nil {
			return err
		}
		resources = append(resources, fname)
	}
	sort.Strings(resources)

	kust := map[string]any{
		"apiVersion": "kustomize.config.k8s.io/v1beta1",
		"kind":       "Kustomization",
		"resources":  resources,
	}
	kb, _ := marshalYAML(kust)
	if err := os.WriteFile(filepath.Join(base, "kustomization.yaml"), kb, 0o644); err != nil {
		return err
	}

	return writeAppReadme(filepath.Join(root, g.project, g.app, "README.md"), g)
}

func writeAppReadme(path string, g *appGroup) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s / %s\n\nGénéré par ocp-toolkit (module migrate). Structure Kustomize : `base/`.\n\n", g.project, g.app)
	fmt.Fprintf(&b, "## Transformations appliquées\n\n")
	n := 0
	for _, res := range g.results {
		for _, a := range res.Actions {
			fmt.Fprintf(&b, "- %s\n", a)
			n++
		}
	}
	if n == 0 {
		b.WriteString("- aucune transformation nécessaire (copie propre)\n")
	}
	b.WriteString("\n## Points nécessitant une revue humaine\n\n")
	n = 0
	for _, res := range g.results {
		for _, w := range res.Warnings {
			fmt.Fprintf(&b, "- ⚠ %s\n", w)
			n++
		}
	}
	if n == 0 {
		b.WriteString("- rien à signaler\n")
	}
	b.WriteString("\n## Données et volumes\n\nCet outil ne migre pas l'état : utiliser **MTC (Migration Toolkit for Containers) / Crane** pour les PV et les données.\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// writeProjects écrit le kustomization.yaml par projet (namespace + apps).
func writeProjects(root string, projects map[string][]string, nsDocs map[string]map[string]any) error {
	for project, apps := range projects {
		dir := filepath.Join(root, project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		resources := []string{}

		nsDoc, ok := nsDocs[project]
		if !ok {
			// Namespace minimal si la source ne le fournissait pas.
			nsDoc = map[string]any{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]any{
					"name": project,
					"annotations": map[string]any{
						"openshift.io/display-name": project,
						"ocp-toolkit/migrated":      "true",
					},
				},
			}
		}
		nb, _ := marshalYAML(nsDoc)
		if err := os.WriteFile(filepath.Join(dir, "namespace.yaml"), nb, 0o644); err != nil {
			return err
		}
		resources = append(resources, "namespace.yaml")

		sort.Strings(apps)
		for _, a := range apps {
			resources = append(resources, a+"/base")
		}
		kust := map[string]any{
			"apiVersion": "kustomize.config.k8s.io/v1beta1",
			"kind":       "Kustomization",
			"resources":  resources,
		}
		kb, _ := marshalYAML(kust)
		if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), kb, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func printSummary(rep *report.Report, dir string) {
	ui.Title("Synthèse migration")
	s := rep.Summary
	ui.Info("Applications : %s%d propres%s / %s%d avec revue requise%s (total %d)",
		ui.Green, s.Pass, ui.Reset, ui.Yellow, s.Warn+s.Fail, ui.Reset, s.Total)

	var rows [][]string
	for _, it := range rep.Items {
		warns := 0
		for _, c := range it.Checks {
			if c.Status == report.Warn {
				warns++
			}
		}
		rows = append(rows, []string{it.Name, ui.StatusBadge(string(it.Status)), fmt.Sprintf("%d warning(s)", warns)})
	}
	if len(rows) > 0 {
		fmt.Println()
		ui.Table([]string{"PROJET/APPLICATION", "STATUT", "REVUE"}, rows)
	}
	fmt.Println()
	ui.Success("Rapport : %s — README par application dans le dossier de sortie", dir)
	ui.Info("Complémentarité Red Hat : Ingress→Route existe aussi en natif à la volée ;")
	ui.Info("MTC/Crane pour la migration des volumes et de l'état. Cet outil = manifestes GitOps propres.")
}
