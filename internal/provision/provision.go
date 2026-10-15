// Package provision — MODULE 2 : génération assistée des configurations
// d'installation OpenShift 4.x (install-config.yaml + agent-config.yaml)
// et orchestration optionnelle d'openshift-install.
//
// Dry-run par défaut : la génération n'écrit que dans generated/ et ne
// touche à rien d'autre. L'installation réelle exige --confirm ET la
// présence du binaire openshift-install.
package provision

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/GhislainAdon/ocp-toolkit/internal/common/logging"
	"github.com/GhislainAdon/ocp-toolkit/internal/common/ui"
)

// Options du module provision.
type Options struct {
	Log         *logging.Logger
	OutputDir   string // generated
	AnswersFile string // réponses YAML pour le mode non-interactif (CI)
	Confirm     bool
	Mirror      string // pré-remplit le registre miroir (--registry-mirror)
}

// Answers regroupe toutes les réponses de l'assistant. Sérialisable en YAML
// pour rejouer une génération en CI (--answers fichier.yaml).
type Answers struct {
	ClusterName     string   `yaml:"clusterName"`
	BaseDomain      string   `yaml:"baseDomain"`
	Platform        string   `yaml:"platform"` // baremetal | vsphere | none
	Masters         int      `yaml:"masters"`
	Workers         int      `yaml:"workers"`
	MachineNetwork  string   `yaml:"machineNetwork"`
	ClusterNetwork  string   `yaml:"clusterNetwork"`
	HostPrefix      int      `yaml:"hostPrefix"`
	ServiceNetwork  string   `yaml:"serviceNetwork"`
	APIVIP          string   `yaml:"apiVIP,omitempty"`
	IngressVIP      string   `yaml:"ingressVIP,omitempty"`
	RendezvousIP    string   `yaml:"rendezvousIP"`
	PullSecretFile  string   `yaml:"pullSecretFile,omitempty"`
	PullSecret      string   `yaml:"pullSecret,omitempty"`
	SSHKeyFile      string   `yaml:"sshKeyFile,omitempty"`
	SSHKey          string   `yaml:"sshKey,omitempty"`
	MirrorRegistry  string   `yaml:"mirrorRegistry,omitempty"`
	TrustBundleFile string   `yaml:"trustBundleFile,omitempty"`
	NTPServers      []string `yaml:"ntpServers,omitempty"`
}

type tmplData struct {
	Answers
	TrustBundle  string
	HostSkeleton []string
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Run exécute le module 2.
func Run(o Options) error {
	ui.Title("Module 2 — Provisioning cluster OpenShift 4.x")

	var a Answers
	if o.AnswersFile != "" {
		b, err := os.ReadFile(o.AnswersFile)
		if err != nil {
			return fmt.Errorf("lecture de %s: %w", o.AnswersFile, err)
		}
		if err := yaml.Unmarshal(b, &a); err != nil {
			return fmt.Errorf("réponses YAML invalides: %w", err)
		}
		ui.Info("Réponses chargées depuis %s (mode non-interactif)", o.AnswersFile)
	} else {
		a = interactiveWizard(o.Mirror)
	}

	if err := loadFiles(&a); err != nil {
		return err
	}
	if errs := validateAnswers(a); len(errs) > 0 {
		for _, e := range errs {
			ui.Error("%s", e)
		}
		return fmt.Errorf("%d erreur(s) de validation — rien n'a été généré", len(errs))
	}

	dir := filepath.Join(o.OutputDir, "install-"+a.ClusterName)
	if err := render(a, dir); err != nil {
		return err
	}
	ui.Success("Configuration générée dans %s", dir)
	ui.Info("  install-config.yaml, agent-config.yaml, README-install.md, answers.yaml, .gitignore")
	ui.Warn("install-config.yaml contient le pull-secret : NE PAS le committer (un .gitignore est en place).")
	o.Log.Info("provision: config generee pour %s.%s dans %s", a.ClusterName, a.BaseDomain, dir)

	if !o.Confirm {
		ui.Info("Mode génération seule (dry-run). Pour enchaîner sur la création de l'ISO : --confirm")
		return nil
	}
	return runInstaller(dir)
}

func interactiveWizard(mirror string) Answers {
	a := Answers{}
	ui.Info("Assistant de configuration — Entrée pour accepter les valeurs par défaut.")
	a.ClusterName = ui.PromptRequired("Nom du cluster (ex. prod-01)")
	a.BaseDomain = ui.PromptRequired("baseDomain (ex. example.com)")
	a.Platform = ui.Choice("Plateforme", []string{"baremetal", "vsphere", "none"}, "baremetal")
	a.Masters, _ = strconv.Atoi(ui.Prompt("Nombre de control-plane", "3"))
	a.Workers, _ = strconv.Atoi(ui.Prompt("Nombre de workers", "3"))
	a.MachineNetwork = ui.Prompt("machineNetwork CIDR", "10.0.0.0/16")
	a.ClusterNetwork = ui.Prompt("clusterNetwork CIDR (pods)", "10.128.0.0/14")
	a.HostPrefix, _ = strconv.Atoi(ui.Prompt("hostPrefix (taille de sous-réseau pod par nœud)", "23"))
	a.ServiceNetwork = ui.Prompt("serviceNetwork CIDR", "172.30.0.0/16")
	if a.Platform == "baremetal" {
		a.APIVIP = ui.PromptRequired("VIP API (dans machineNetwork)")
		a.IngressVIP = ui.PromptRequired("VIP Ingress (dans machineNetwork)")
	}
	a.RendezvousIP = ui.PromptRequired("rendezvousIP (IP du 1er master, Agent-based Installer)")
	a.PullSecretFile = ui.Prompt("Chemin du pull-secret (fichier JSON)", "pull-secret.json")
	a.SSHKeyFile = ui.Prompt("Chemin de la clé SSH publique", filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519.pub"))
	if ui.Confirm("Environnement air-gapped / registre miroir ?") || mirror != "" {
		a.MirrorRegistry = ui.Prompt("Registre miroir (host:port)", mirror)
		a.TrustBundleFile = ui.Prompt("Fichier CA du miroir (PEM, vide si CA publique)", "")
	}
	if ntp := ui.Prompt("Serveurs NTP additionnels (séparés par des virgules, vide sinon)", ""); ntp != "" {
		for _, s := range strings.Split(ntp, ",") {
			a.NTPServers = append(a.NTPServers, strings.TrimSpace(s))
		}
	}
	return a
}

// loadFiles résout pull-secret et clé SSH depuis les fichiers indiqués.
func loadFiles(a *Answers) error {
	if a.PullSecret == "" && a.PullSecretFile != "" {
		b, err := os.ReadFile(a.PullSecretFile)
		if err != nil {
			return fmt.Errorf("pull-secret illisible (%s): %w", a.PullSecretFile, err)
		}
		a.PullSecret = strings.TrimSpace(string(b))
	}
	if a.SSHKey == "" && a.SSHKeyFile != "" {
		b, err := os.ReadFile(a.SSHKeyFile)
		if err != nil {
			return fmt.Errorf("clé SSH illisible (%s): %w", a.SSHKeyFile, err)
		}
		a.SSHKey = strings.TrimSpace(string(b))
	}
	return nil
}

// validateAnswers vérifie schéma et champs obligatoires AVANT toute écriture.
func validateAnswers(a Answers) []string {
	var errs []string
	if !nameRe.MatchString(a.ClusterName) {
		errs = append(errs, fmt.Sprintf("nom de cluster invalide %q (minuscules, chiffres, tirets)", a.ClusterName))
	}
	if a.BaseDomain == "" || !strings.Contains(a.BaseDomain, ".") {
		errs = append(errs, fmt.Sprintf("baseDomain invalide %q", a.BaseDomain))
	}
	if a.Masters != 3 && a.Masters != 1 {
		errs = append(errs, fmt.Sprintf("control-plane: %d réplicas (3 pour la prod, 1 = SNO uniquement)", a.Masters))
	}
	if a.Workers < 0 {
		errs = append(errs, "workers < 0")
	}
	for label, cidr := range map[string]string{
		"machineNetwork": a.MachineNetwork, "clusterNetwork": a.ClusterNetwork, "serviceNetwork": a.ServiceNetwork,
	} {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			errs = append(errs, fmt.Sprintf("%s : CIDR invalide %q", label, cidr))
		}
	}
	if a.HostPrefix < 8 || a.HostPrefix > 27 {
		errs = append(errs, fmt.Sprintf("hostPrefix %d hors plage raisonnable (23 recommandé)", a.HostPrefix))
	}
	if a.Platform == "baremetal" {
		for label, ip := range map[string]string{"VIP API": a.APIVIP, "VIP Ingress": a.IngressVIP} {
			if net.ParseIP(ip) == nil {
				errs = append(errs, fmt.Sprintf("%s invalide: %q", label, ip))
			}
		}
	}
	if net.ParseIP(a.RendezvousIP) == nil {
		errs = append(errs, fmt.Sprintf("rendezvousIP invalide: %q", a.RendezvousIP))
	}
	if a.PullSecret == "" || !json.Valid([]byte(a.PullSecret)) {
		errs = append(errs, "pull-secret vide ou JSON invalide")
	}
	if !strings.HasPrefix(a.SSHKey, "ssh-") && !strings.HasPrefix(a.SSHKey, "ecdsa-") {
		errs = append(errs, "clé SSH publique invalide (doit commencer par ssh-… )")
	}
	return errs
}

func render(a Answers, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data := tmplData{Answers: a}
	if a.TrustBundleFile != "" {
		b, err := os.ReadFile(a.TrustBundleFile)
		if err != nil {
			return fmt.Errorf("trust bundle illisible: %w", err)
		}
		// Indentation YAML du bloc PEM (2 espaces).
		var sb strings.Builder
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			sb.WriteString("  " + l + "\n")
		}
		data.TrustBundle = strings.TrimRight(sb.String(), "\n")
	}
	for i := 0; i < a.Masters; i++ {
		data.HostSkeleton = append(data.HostSkeleton, fmt.Sprintf("master-%d", i))
	}

	files := map[string]string{
		"install-config.yaml": installConfigTmpl,
		"agent-config.yaml":   agentConfigTmpl,
		"README-install.md":   installReadmeTmpl,
	}
	for name, tmpl := range files {
		t, err := template.New(name).Parse(tmpl)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return err
		}
		// Contrôle final : le YAML généré doit se parser.
		if strings.HasSuffix(name, ".yaml") {
			var check map[string]any
			if err := yaml.Unmarshal(buf.Bytes(), &check); err != nil {
				return fmt.Errorf("%s généré invalide (bug de template ?): %w", name, err)
			}
		}
		mode := os.FileMode(0o644)
		if name == "install-config.yaml" {
			mode = 0o600 // contient le pull-secret
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), mode); err != nil {
			return err
		}
	}

	// Rejouabilité : réponses sauvegardées SANS les secrets.
	replay := a
	replay.PullSecret = ""
	replay.SSHKey = ""
	rb, _ := yaml.Marshal(replay)
	if err := os.WriteFile(filepath.Join(dir, "answers.yaml"), rb, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignoreContent), 0o644)
}

// runInstaller orchestre openshift-install (mode --confirm uniquement).
func runInstaller(dir string) error {
	if _, err := exec.LookPath("openshift-install"); err != nil {
		ui.Warn("binaire openshift-install introuvable dans le PATH — voir README-install.md pour les étapes manuelles")
		return nil
	}
	if !ui.Confirm(fmt.Sprintf("Lancer 'openshift-install agent create image --dir %s' maintenant ?", dir)) {
		ui.Info("Abandon — la configuration générée reste utilisable manuellement.")
		return nil
	}
	// agent-config.yaml doit avoir sa section hosts complétée.
	ui.Warn("Vérifier que la section hosts: d'agent-config.yaml est complétée (MAC des nœuds).")
	steps := [][]string{
		{"agent", "create", "image", "--dir", dir},
	}
	for _, args := range steps {
		ui.Info("openshift-install %s", strings.Join(args, " "))
		cmd := exec.Command("openshift-install", args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("openshift-install %s: %w", strings.Join(args, " "), err)
		}
	}
	ui.Success("ISO générée. Après boot des nœuds, suivre :")
	ui.Info("  openshift-install agent wait-for bootstrap-complete --dir %s", dir)
	ui.Info("  openshift-install agent wait-for install-complete --dir %s", dir)
	return nil
}
