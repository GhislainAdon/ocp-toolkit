package report

import (
	"bytes"
	"html/template"
)

// Rapport HTML autonome (CSS inline, zéro asset externe — air-gap friendly).

const htmlTmpl = `<!DOCTYPE html>
<html lang="fr">
<head>
<meta charset="utf-8">
<title>ocp-toolkit — {{ .Kind }} {{ .Timestamp }}</title>
<style>
  :root { --pass:#1a7f37; --fail:#cf222e; --warn:#9a6700; --skip:#57606a; }
  body { font-family: -apple-system, "Segoe UI", Roboto, sans-serif; margin: 2rem auto; max-width: 1100px; color: #1f2328; padding: 0 1rem; }
  h1 { font-size: 1.4rem; } h2 { font-size: 1.1rem; margin-top: 2rem; }
  .meta { color: #57606a; font-size: .9rem; }
  .cards { display: flex; gap: 1rem; margin: 1.2rem 0; flex-wrap: wrap; }
  .card { border: 1px solid #d0d7de; border-radius: 8px; padding: .8rem 1.4rem; min-width: 90px; text-align: center; }
  .card b { display: block; font-size: 1.6rem; }
  .pass b { color: var(--pass); } .fail b { color: var(--fail); } .warn b { color: var(--warn); } .skip b { color: var(--skip); }
  table { border-collapse: collapse; width: 100%; font-size: .88rem; }
  th, td { border: 1px solid #d0d7de; padding: .35rem .6rem; text-align: left; vertical-align: top; }
  th { background: #f6f8fa; }
  .badge { font-weight: 600; padding: .05rem .5rem; border-radius: 10px; font-size: .78rem; color: #fff; white-space: nowrap; }
  .b-PASS { background: var(--pass); } .b-FAIL { background: var(--fail); } .b-WARN { background: var(--warn); color:#fff; } .b-SKIP { background: var(--skip); }
  .diff ul { margin: .2rem 0 .8rem 1.2rem; }
  .notes li { color: #57606a; }
  details { margin: .2rem 0; }
  .checkdetail { color: #57606a; }
</style>
</head>
<body>
<h1>ocp-toolkit — rapport « {{ .Kind }} »</h1>
<p class="meta">Horodatage : {{ .Timestamp }}{{ if .Cluster }} — contexte : {{ .Cluster }}{{ end }}{{ if .DryRun }} — <b>DRY-RUN</b>{{ end }}</p>

<div class="cards">
  <div class="card"><b>{{ .Summary.Total }}</b>total</div>
  <div class="card pass"><b>{{ .Summary.Pass }}</b>PASS</div>
  <div class="card fail"><b>{{ .Summary.Fail }}</b>FAIL</div>
  <div class="card warn"><b>{{ .Summary.Warn }}</b>WARN</div>
  <div class="card skip"><b>{{ .Summary.Skip }}</b>SKIP</div>
</div>

{{ if .Diff }}
<h2>Évolution depuis la vague {{ .Diff.Previous }}</h2>
<div class="diff">
  {{ if .Diff.Regressed }}<b>Régressions ({{ len .Diff.Regressed }})</b><ul>{{ range .Diff.Regressed }}<li>{{ . }}</li>{{ end }}</ul>{{ end }}
  {{ if .Diff.NewFail }}<b>Nouveaux échecs ({{ len .Diff.NewFail }})</b><ul>{{ range .Diff.NewFail }}<li>{{ . }}</li>{{ end }}</ul>{{ end }}
  {{ if .Diff.Fixed }}<b>Corrigés ({{ len .Diff.Fixed }})</b><ul>{{ range .Diff.Fixed }}<li>{{ . }}</li>{{ end }}</ul>{{ end }}
  {{ if .Diff.Persistent }}<b>Échecs persistants ({{ len .Diff.Persistent }})</b><ul>{{ range .Diff.Persistent }}<li>{{ . }}</li>{{ end }}</ul>{{ end }}
  {{ if not .Diff.NewFail }}{{ if not .Diff.Regressed }}{{ if not .Diff.Fixed }}{{ if not .Diff.Persistent }}<p>Aucun changement notable.</p>{{ end }}{{ end }}{{ end }}{{ end }}
</div>
{{ end }}

{{ if .Notes }}
<h2>Notes</h2>
<ul class="notes">{{ range .Notes }}<li>{{ . }}</li>{{ end }}</ul>
{{ end }}

<h2>Détail ({{ len .Items }})</h2>
<table>
  <tr><th>Élément</th><th>Statut</th><th>Checks</th></tr>
  {{ range .Items }}
  <tr>
    <td>{{ .Name }}</td>
    <td><span class="badge b-{{ .Status }}">{{ .Status }}</span></td>
    <td>
      {{ range .Checks }}
      <details{{ if ne .Status "PASS" }} open{{ end }}>
        <summary><span class="badge b-{{ .Status }}">{{ .Status }}</span> {{ .Name }}</summary>
        <span class="checkdetail">{{ .Detail }}</span>
      </details>
      {{ end }}
    </td>
  </tr>
  {{ end }}
</table>
</body>
</html>`

func renderHTML(r *Report) ([]byte, error) {
	t, err := template.New("report").Parse(htmlTmpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
