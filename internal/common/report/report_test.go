package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalizeWorstStatus(t *testing.T) {
	r := New("validate", "test")
	r.Items = []Item{
		{Name: "n1", Checks: []Check{{Name: "a", Status: Pass}, {Name: "b", Status: Pass}}},
		{Name: "n2", Checks: []Check{{Name: "a", Status: Pass}, {Name: "b", Status: Fail}}},
		{Name: "n3", Checks: []Check{{Name: "a", Status: Warn}, {Name: "b", Status: Pass}}},
		{Name: "n4", Checks: []Check{{Name: "a", Status: Skip}}},
	}
	r.Finalize()
	want := map[string]Status{"n1": Pass, "n2": Fail, "n3": Warn, "n4": Skip}
	for _, it := range r.Items {
		if it.Status != want[it.Name] {
			t.Errorf("%s: statut %s, attendu %s", it.Name, it.Status, want[it.Name])
		}
	}
	if r.Summary.Pass != 1 || r.Summary.Fail != 1 || r.Summary.Warn != 1 || r.Summary.Skip != 1 {
		t.Errorf("summary inattendu: %+v", r.Summary)
	}
}

func TestDiffBetweenWaves(t *testing.T) {
	prev := &Report{Timestamp: "20260701-100000", Items: []Item{
		{Name: "node-a", Status: Fail},
		{Name: "node-b", Status: Pass},
		{Name: "node-c", Status: Fail},
	}}
	cur := &Report{Items: []Item{
		{Name: "node-a", Status: Pass}, // corrigé
		{Name: "node-b", Status: Fail}, // régression
		{Name: "node-c", Status: Fail}, // persistant
		{Name: "node-d", Status: Fail}, // nouveau
	}}
	d := ComputeDiff(prev, cur)
	if len(d.Fixed) != 1 || d.Fixed[0] != "node-a" {
		t.Errorf("Fixed = %v", d.Fixed)
	}
	if len(d.Regressed) != 1 || d.Regressed[0] != "node-b" {
		t.Errorf("Regressed = %v", d.Regressed)
	}
	if len(d.Persistent) != 1 || d.Persistent[0] != "node-c" {
		t.Errorf("Persistent = %v", d.Persistent)
	}
	if len(d.NewFail) != 1 || d.NewFail[0] != "node-d" {
		t.Errorf("NewFail = %v", d.NewFail)
	}
	if ComputeDiff(nil, cur) != nil {
		t.Error("diff sans précédent doit être nil")
	}
}

func TestWriteAndLoadPrevious(t *testing.T) {
	root := t.TempDir()
	r := New("validate", "ctx")
	r.Items = []Item{{Name: "n1", Checks: []Check{
		{Name: "ready", Status: Pass, Detail: "ok"},
		{Name: "dns", Status: Fail, Detail: "timeout"},
	}}}
	r.Finalize()
	dir, err := r.Write(root)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, f := range []string{"report.json", "report.html", "junit.xml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("fichier manquant %s: %v", f, err)
		}
	}
	// JUnit doit contenir un failure.
	b, _ := os.ReadFile(filepath.Join(dir, "junit.xml"))
	if !strings.Contains(string(b), "<failure") {
		t.Error("junit.xml devrait contenir un <failure>")
	}
	// LoadPrevious retrouve ce rapport.
	prev, err := LoadPrevious(root, "validate")
	if err != nil || prev == nil {
		t.Fatalf("LoadPrevious: %v, prev=%v", err, prev)
	}
	if prev.Items[0].Name != "n1" {
		t.Errorf("item = %q", prev.Items[0].Name)
	}
	if p, _ := LoadPrevious(root, "migrate"); p != nil {
		t.Error("aucun rapport migrate attendu")
	}
}
