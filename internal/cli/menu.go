package cli

import (
	"fmt"

	"github.com/GhislainAdon/ocp-toolkit/internal/common/ui"
	"github.com/GhislainAdon/ocp-toolkit/internal/migrate"
	"github.com/GhislainAdon/ocp-toolkit/internal/provision"
	"github.com/GhislainAdon/ocp-toolkit/internal/validate"
)

// runMenu affiche le menu principal (lancement sans argument).
func runMenu() error {
	for {
		fmt.Printf(`
%s=== OpenShift Migration Toolkit (ocptk) ===%s
1) Validation des nœuds migrés            (tests + rapport)
2) Provisioning cluster OpenShift 4.x     (génération config + install)
3) Migration d'applications K8s → OpenShift
q) Quitter
`, ui.Bold, ui.Reset)

		switch ui.Prompt("Choix", "q") {
		case "1":
			if err := menuValidate(); err != nil {
				ui.Error("%v", err)
			}
		case "2":
			if err := menuProvision(); err != nil {
				ui.Error("%v", err)
			}
		case "3":
			if err := menuMigrate(); err != nil {
				ui.Error("%v", err)
			}
		case "q", "Q":
			return nil
		default:
			ui.Warn("choix invalide")
		}
	}
}

func menuValidate() error {
	confirm := ui.Confirm("Exécution réelle (déploiement du DaemonSet de test) ? Non = dry-run")
	g.confirm = confirm
	c, log, err := newKube(true)
	if err != nil {
		return err
	}
	defer log.Close()
	o := validate.Options{
		Kube: c, Log: log,
		ReportsDir: g.reportsDir, ManifestsDir: "manifests",
		Confirm:        confirm,
		NodeSelector:   ui.Prompt("Sélecteur de nœuds (k=v, vide = tous)", ""),
		RegistryMirror: g.mirror,
	}
	if confirm {
		o.TolerateAll = ui.Confirm("Inclure masters/infra (tolérer toutes les taints) ?")
		if sc := ui.Prompt("StorageClass pour le test PVC échantillonné (vide = ignorer)", ""); sc != "" {
			o.PVCStorageClass = sc
		}
	}
	return validate.Run(o)
}

func menuProvision() error {
	log := newLogger()
	defer log.Close()
	return provision.Run(provision.Options{
		Log: log, OutputDir: "generated",
		Confirm: ui.Confirm("Lancer l'installation après génération (openshift-install requis) ? Non = génération seule"),
		Mirror:  g.mirror,
	})
}

func menuMigrate() error {
	o := migrate.Options{
		ReportsDir: g.reportsDir,
		OutputDir:  "generated/migrate",
	}
	if ui.Confirm("Lire depuis un cluster source ? (Non = dossier de manifestes YAML)") {
		o.FromCluster = true
		nsRaw := ui.PromptRequired("Namespaces à exporter (séparés par des virgules)")
		o.Namespaces = append(o.Namespaces, splitCSV(nsRaw)...)
		g.confirm = true // lecture seule côté cluster ; l'écriture locale est confirmée plus bas
		c, log, err := newKube(true)
		if err != nil {
			return err
		}
		defer log.Close()
		c.DryRun = false // uniquement des `get`
		o.Kube, o.Log = c, log
	} else {
		o.SourceDir = ui.PromptRequired("Dossier des manifestes source")
		log := newLogger()
		defer log.Close()
		o.Log = log
	}
	o.TOpts.FixSCC = ui.Confirm("Corriger automatiquement les runAsUser/fsGroup en dur (--fix-scc) ?")
	o.TOpts.FixNodePort = ui.Confirm("Convertir les Service NodePort en ClusterIP (--fix-nodeport) ?")
	o.Confirm = ui.Confirm("Écrire l'arborescence Kustomize dans generated/migrate ? Non = rapport seul")
	return migrate.Run(o)
}

func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := trim(s[start:i])
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
