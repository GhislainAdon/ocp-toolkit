// Package cli — commande racine ocptk, flags globaux et sous-commandes.
// Sans argument : menu interactif. Avec sous-commande : automatisable en CI.
package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/GhislainAdon/ocp-toolkit/internal/common/kube"
	"github.com/GhislainAdon/ocp-toolkit/internal/common/logging"
	"github.com/GhislainAdon/ocp-toolkit/internal/migrate"
	"github.com/GhislainAdon/ocp-toolkit/internal/provision"
	"github.com/GhislainAdon/ocp-toolkit/internal/validate"
)

// Version du binaire (surchargée par -ldflags "-X ….Version=…").
var Version = "0.1.0"

// global regroupe les flags persistants partagés par tous les modules.
type global struct {
	context    string
	kubeconfig string
	confirm    bool
	reportsDir string
	logsDir    string
	mirror     string
}

var g global

// Execute construit l'arbre de commandes et exécute.
func Execute() error {
	root := &cobra.Command{
		Use:   "ocptk",
		Short: "OpenShift Migration Toolkit — validation, provisioning, migration",
		Long: "ocp-toolkit assiste la migration de nœuds Kubernetes vanilla vers OpenShift 4.x\n" +
			"à grande échelle (5000+ nœuds). Sans argument : menu interactif.\n" +
			"Toute action mutante est en dry-run par défaut ; exécution réelle avec --confirm.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMenu()
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&g.context, "context", "", "contexte kubeconfig à utiliser")
	pf.StringVar(&g.kubeconfig, "kubeconfig", "", "chemin du kubeconfig")
	pf.BoolVar(&g.confirm, "confirm", false, "exécution réelle (sinon dry-run)")
	pf.StringVar(&g.reportsDir, "reports-dir", "reports", "répertoire des rapports horodatés")
	pf.StringVar(&g.logsDir, "logs-dir", "logs", "répertoire des fichiers de log")
	pf.StringVar(&g.mirror, "registry-mirror", "", "registre miroir pour environnements air-gapped")

	root.AddCommand(validateCmd(), provisionCmd(), migrateCmd())
	return root.Execute()
}

// newLogger crée le logger fichier de l'exécution (best-effort).
func newLogger() *logging.Logger {
	l, err := logging.New(g.logsDir)
	if err != nil {
		fmt.Printf("(log fichier indisponible: %v)\n", err)
		return nil
	}
	return l
}

func newKube(required bool) (*kube.Client, *logging.Logger, error) {
	log := newLogger()
	logf := func(format string, a ...any) { log.Info(format, a...) }
	c, err := kube.Detect(g.context, g.kubeconfig, !g.confirm, logf)
	if err != nil && required {
		return nil, log, err
	}
	return c, log, nil
}

func validateCmd() *cobra.Command {
	o := validate.Options{}
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Module 1 — validation des nœuds migrés (DaemonSet de smoke-tests + rapport)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, log, err := newKube(true)
			if err != nil {
				return err
			}
			defer log.Close()
			o.Kube, o.Log = c, log
			o.ReportsDir, o.Confirm = g.reportsDir, g.confirm
			o.RegistryMirror = g.mirror
			return validate.Run(o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.NodeSelector, "node-selector", "", "sélecteur de nœuds k=v,k=v (défaut: tous)")
	f.StringVar(&o.Image, "image", "", "image des pods de test (défaut: ubi9-minimal, réécrite si --registry-mirror)")
	f.StringVar(&o.EgressTarget, "egress-target", "", "cible host:port du test egress (désactivé sinon)")
	f.StringVar(&o.PVCStorageClass, "pvc-storageclass", "", "StorageClass du test PVC échantillonné (désactivé sinon)")
	f.IntVar(&o.PVCSample, "pvc-sample", 5, "nombre de nœuds échantillonnés pour le test PVC")
	f.IntVar(&o.Parallel, "parallel", 30, "collectes de logs simultanées")
	f.IntVar(&o.TimeoutMin, "timeout", 15, "timeout du rollout DaemonSet (minutes)")
	f.BoolVar(&o.TolerateAll, "tolerate-all", false, "tolérer toutes les taints (inclut masters/infra)")
	f.BoolVar(&o.Keep, "keep", false, "conserver le namespace de test après collecte")
	f.BoolVar(&o.OPCT, "opct", false, "afficher/orchestrer la conformance cluster OPCT")
	f.StringVar(&o.ManifestsDir, "manifests-dir", "manifests", "répertoire de sortie des manifestes générés")
	f.StringVar(&o.Namespace, "namespace", "ocptk-validate", "namespace éphémère des smoke-tests")
	return cmd
}

func provisionCmd() *cobra.Command {
	o := provision.Options{}
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Module 2 — génération config d'installation OpenShift 4.x (+ install optionnelle)",
		RunE: func(cmd *cobra.Command, args []string) error {
			log := newLogger()
			defer log.Close()
			o.Log = log
			o.Confirm = g.confirm
			o.Mirror = g.mirror
			return provision.Run(o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.AnswersFile, "answers", "", "fichier YAML de réponses (mode non-interactif / CI)")
	f.StringVar(&o.OutputDir, "output", "generated", "répertoire de sortie")
	return cmd
}

func migrateCmd() *cobra.Command {
	o := migrate.Options{}
	var imageRewrites, scMap []string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Module 3 — transformation d'applications K8s → OpenShift (sortie Kustomize/GitOps)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			required := o.FromCluster
			c, log, err := newKube(required)
			if err != nil {
				return err
			}
			defer log.Close()
			o.Kube, o.Log = c, log
			o.ReportsDir, o.Confirm = g.reportsDir, g.confirm
			o.TOpts.ImageRewrites, err = parseKV(imageRewrites)
			if err != nil {
				return fmt.Errorf("--image-rewrite: %w", err)
			}
			o.TOpts.StorageClassMap, err = parseKV(scMap)
			if err != nil {
				return fmt.Errorf("--storage-class-map: %w", err)
			}
			if g.mirror != "" && len(o.TOpts.ImageRewrites) == 0 {
				// Raccourci air-gap : préfixer les registres publics usuels.
				o.TOpts.ImageRewrites = map[string]string{
					"docker.io": g.mirror, "quay.io": g.mirror, "registry.k8s.io": g.mirror,
				}
			}
			return migrate.Run(o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.SourceDir, "source-dir", "", "dossier de manifestes YAML source")
	f.BoolVar(&o.FromCluster, "from-cluster", false, "lire les ressources depuis le cluster courant")
	f.StringSliceVar(&o.Namespaces, "namespaces", nil, "namespaces à exporter (avec --from-cluster)")
	f.StringVar(&o.OutputDir, "output", "generated/migrate", "répertoire de sortie Kustomize")
	f.StringSliceVar(&imageRewrites, "image-rewrite", nil, "réécriture d'image source=cible (répétable)")
	f.StringSliceVar(&scMap, "storage-class-map", nil, "mapping StorageClass source=cible (répétable)")
	f.BoolVar(&o.TOpts.FixSCC, "fix-scc", false, "supprimer les runAsUser/fsGroup en dur (sinon WARN)")
	f.BoolVar(&o.TOpts.FixNodePort, "fix-nodeport", false, "convertir les Service NodePort en ClusterIP")
	return cmd
}

func parseKV(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, p := range pairs {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("format attendu source=cible, reçu %q", p)
		}
		out[parts[0]] = parts[1]
	}
	return out, nil
}
