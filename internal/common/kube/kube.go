// Package kube — wrapper fin autour de `oc` / `kubectl` (shell-out).
//
// Choix délibéré : pas de client-go. Le shell-out vers oc/kubectl garde le
// binaire léger (~8 Mo vs ~40 Mo), profite gratuitement de l'auth kubeconfig
// (exec plugins, OIDC, tokens Teleport…) et des sous-commandes natives
// OpenShift (oc). L'outil détecte `oc` en priorité, sinon `kubectl`.
package kube

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Client exécute des commandes oc/kubectl avec contexte/kubeconfig fixés.
type Client struct {
	Bin        string
	Context    string
	Kubeconfig string
	// DryRun ajoute --dry-run=client aux apply.
	DryRun bool
	// Logf journalise chaque commande exécutée (jamais nil après Detect).
	Logf func(format string, a ...any)
}

// Detect trouve oc (prioritaire) ou kubectl dans le PATH.
func Detect(context, kubeconfig string, dryRun bool, logf func(string, ...any)) (*Client, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	for _, bin := range []string{"oc", "kubectl"} {
		if _, err := exec.LookPath(bin); err == nil {
			return &Client{Bin: bin, Context: context, Kubeconfig: kubeconfig, DryRun: dryRun, Logf: logf}, nil
		}
	}
	return nil, fmt.Errorf("ni 'oc' ni 'kubectl' trouvés dans le PATH")
}

func (c *Client) baseArgs() []string {
	var a []string
	if c.Context != "" {
		a = append(a, "--context", c.Context)
	}
	if c.Kubeconfig != "" {
		a = append(a, "--kubeconfig", c.Kubeconfig)
	}
	return a
}

// Run exécute la commande et retourne stdout. stderr est intégré à l'erreur.
func (c *Client) Run(stdin []byte, args ...string) (string, error) {
	full := append(c.baseArgs(), args...)
	c.Logf("exec: %s %s", c.Bin, strings.Join(full, " "))
	cmd := exec.Command(c.Bin, full...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w — %s",
			c.Bin, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Apply applique un manifeste multi-documents via stdin.
// Idempotent par construction (apply + labels gérés par l'outil).
func (c *Client) Apply(manifest []byte) (string, error) {
	args := []string{"apply", "-f", "-"}
	if c.DryRun {
		args = append(args, "--dry-run=client")
	}
	return c.Run(manifest, args...)
}

// GetJSON exécute `get … -o json` et décode dans out.
func (c *Client) GetJSON(out any, args ...string) error {
	s, err := c.Run(nil, append(args, "-o", "json")...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), out)
}

// DeleteIgnore supprime en ignorant l'absence (nettoyage best-effort).
func (c *Client) DeleteIgnore(args ...string) {
	_, _ = c.Run(nil, append([]string{"delete", "--ignore-not-found=true", "--wait=false"}, args...)...)
}

// ServerReachable vérifie que l'API répond.
func (c *Client) ServerReachable() bool {
	_, err := c.Run(nil, "version", "-o", "json")
	return err == nil
}
