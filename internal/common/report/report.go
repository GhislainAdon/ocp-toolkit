// Package report — génération des rapports horodatés (JSON machine,
// HTML humain, JUnit XML pour CI) et diff entre vagues de migration.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Status d'un check individuel.
type Status string

// Statuts possibles d'un check.
const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Warn Status = "WARN"
	Skip Status = "SKIP"
)

// Check est un contrôle unitaire (par nœud ou par ressource).
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Item regroupe les checks d'une entité (un nœud, une application…).
type Item struct {
	Name   string  `json:"name"`
	Status Status  `json:"status"`
	Checks []Check `json:"checks"`
}

// Summary agrège les compteurs par statut d'Item.
type Summary struct {
	Total int `json:"total"`
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Warn  int `json:"warn"`
	Skip  int `json:"skip"`
}

// Report est le document racine sérialisé en JSON/HTML/JUnit.
type Report struct {
	Tool      string   `json:"tool"`
	Kind      string   `json:"kind"` // validate | migrate | provision
	Timestamp string   `json:"timestamp"`
	Cluster   string   `json:"cluster,omitempty"`
	DryRun    bool     `json:"dryRun"`
	Items     []Item   `json:"items"`
	Summary   Summary  `json:"summary"`
	Notes     []string `json:"notes,omitempty"`
	Diff      *Diff    `json:"diff,omitempty"`
}

// New crée un rapport horodaté.
func New(kind, cluster string) *Report {
	return &Report{
		Tool:      "ocp-toolkit",
		Kind:      kind,
		Cluster:   cluster,
		Timestamp: time.Now().Format("20060102-150405"),
	}
}

// worst retourne le statut agrégé d'une liste de checks.
func worst(checks []Check) Status {
	st := Skip
	for _, c := range checks {
		switch c.Status {
		case Fail:
			return Fail
		case Warn:
			st = Warn
		case Pass:
			if st == Skip {
				st = Pass
			}
		}
	}
	return st
}

// Finalize calcule le statut de chaque item puis le résumé, et trie par nom.
func (r *Report) Finalize() {
	sort.Slice(r.Items, func(i, j int) bool { return r.Items[i].Name < r.Items[j].Name })
	r.Summary = Summary{Total: len(r.Items)}
	for i := range r.Items {
		r.Items[i].Status = worst(r.Items[i].Checks)
		switch r.Items[i].Status {
		case Pass:
			r.Summary.Pass++
		case Fail:
			r.Summary.Fail++
		case Warn:
			r.Summary.Warn++
		default:
			r.Summary.Skip++
		}
	}
}

// Write écrit report.json + report.html + junit.xml dans
// <root>/<kind>-<timestamp>/ et retourne ce répertoire.
func (r *Report) Write(root string) (string, error) {
	dir := filepath.Join(root, fmt.Sprintf("%s-%s", r.Kind, r.Timestamp))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	j, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), j, 0o644); err != nil {
		return "", err
	}
	h, err := renderHTML(r)
	if err != nil {
		return "", fmt.Errorf("rendu HTML: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.html"), h, 0o644); err != nil {
		return "", err
	}
	x, err := junitXML(r)
	if err != nil {
		return "", fmt.Errorf("rendu JUnit: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "junit.xml"), x, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// LoadPrevious charge le rapport le plus récent du même kind dans root
// (pour le diff entre vagues). Retourne nil sans erreur s'il n'y en a pas.
func LoadPrevious(root, kind string) (*Report, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil // pas encore de répertoire reports/
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), kind+"-") {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(root, d, "report.json"))
		if err != nil {
			continue
		}
		var prev Report
		if err := json.Unmarshal(b, &prev); err != nil {
			continue
		}
		return &prev, nil
	}
	return nil, nil
}
