package report

// Diff compare la vague courante avec la précédente.
type Diff struct {
	Previous   string   `json:"previous"`             // timestamp du rapport comparé
	NewFail    []string `json:"newFail,omitempty"`    // items en échec absents de la vague précédente
	Regressed  []string `json:"regressed,omitempty"`  // OK avant, FAIL maintenant
	Fixed      []string `json:"fixed,omitempty"`      // FAIL avant, OK maintenant
	Persistent []string `json:"persistent,omitempty"` // FAIL avant et maintenant
}

// ComputeDiff calcule le diff cur vs prev. Retourne nil si prev est nil.
func ComputeDiff(prev, cur *Report) *Diff {
	if prev == nil {
		return nil
	}
	pm := make(map[string]Status, len(prev.Items))
	for _, it := range prev.Items {
		pm[it.Name] = it.Status
	}
	d := &Diff{Previous: prev.Timestamp}
	for _, it := range cur.Items {
		p, known := pm[it.Name]
		switch {
		case it.Status == Fail && !known:
			d.NewFail = append(d.NewFail, it.Name)
		case it.Status == Fail && p == Fail:
			d.Persistent = append(d.Persistent, it.Name)
		case it.Status == Fail:
			d.Regressed = append(d.Regressed, it.Name)
		case known && p == Fail:
			d.Fixed = append(d.Fixed, it.Name)
		}
	}
	return d
}

// Empty indique un diff sans aucun changement notable.
func (d *Diff) Empty() bool {
	return d == nil ||
		(len(d.NewFail) == 0 && len(d.Regressed) == 0 && len(d.Fixed) == 0 && len(d.Persistent) == 0)
}
