// Package ui — helpers d'affichage console (couleurs ANSI, prompts, tableaux).
// Zéro dépendance externe : fonctionne dans n'importe quel terminal, y compris
// air-gapped. NO_COLOR est respecté.
package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

var (
	Reset  = "\033[0m"
	Bold   = "\033[1m"
	Dim    = "\033[2m"
	Red    = "\033[31m"
	Green  = "\033[32m"
	Yellow = "\033[33m"
	Cyan   = "\033[36m"
)

var stdin = bufio.NewReader(os.Stdin)

func init() {
	if os.Getenv("NO_COLOR") != "" {
		Reset, Bold, Dim, Red, Green, Yellow, Cyan = "", "", "", "", "", "", ""
	}
}

// Title affiche un titre de section.
func Title(s string) {
	fmt.Printf("\n%s%s=== %s ===%s\n", Bold, Cyan, s, Reset)
}

// Info affiche une ligne d'information indentée.
func Info(format string, a ...any) {
	fmt.Printf("  "+format+"\n", a...)
}

// Success affiche un message de succès.
func Success(format string, a ...any) {
	fmt.Printf("%s[OK]%s "+format+"\n", append([]any{Green, Reset}, a...)...)
}

// Warn affiche un avertissement.
func Warn(format string, a ...any) {
	fmt.Printf("%s[!!]%s "+format+"\n", append([]any{Yellow, Reset}, a...)...)
}

// Error affiche une erreur sur stderr.
func Error(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s[KO]%s "+format+"\n", append([]any{Red, Reset}, a...)...)
}

// Prompt lit une valeur avec défaut optionnel.
func Prompt(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s%s%s] : ", label, Dim, def, Reset)
	} else {
		fmt.Printf("%s : ", label)
	}
	line, _ := stdin.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// PromptRequired insiste jusqu'à obtenir une valeur non vide.
func PromptRequired(label string) string {
	for {
		if v := Prompt(label, ""); v != "" {
			return v
		}
		Warn("valeur obligatoire")
	}
}

// Confirm pose une question fermée, défaut = non.
func Confirm(label string) bool {
	v := strings.ToLower(Prompt(label+" (o/N)", "n"))
	return v == "o" || v == "oui" || v == "y" || v == "yes"
}

// Choice propose une liste de valeurs.
func Choice(label string, options []string, def string) string {
	for {
		v := Prompt(fmt.Sprintf("%s (%s)", label, strings.Join(options, "/")), def)
		for _, o := range options {
			if strings.EqualFold(v, o) {
				return o
			}
		}
		Warn("valeur invalide, choix possibles : %s", strings.Join(options, ", "))
	}
}

// Table affiche un tableau aligné à largeur de colonnes automatique.
func Table(headers []string, rows [][]string) {
	w := make([]int, len(headers))
	for i, h := range headers {
		w[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(w) && len(stripANSI(c)) > w[i] {
				w[i] = len(stripANSI(c))
			}
		}
	}
	line := func(cells []string, bold bool) {
		parts := make([]string, 0, len(cells))
		for i, c := range cells {
			pad := w[i] - len(stripANSI(c))
			if pad < 0 {
				pad = 0
			}
			parts = append(parts, c+strings.Repeat(" ", pad))
		}
		s := "  " + strings.Join(parts, "  ")
		if bold {
			s = Bold + s + Reset
		}
		fmt.Println(s)
	}
	line(headers, true)
	sep := make([]string, len(headers))
	for i := range sep {
		sep[i] = strings.Repeat("-", w[i])
	}
	line(sep, false)
	for _, r := range rows {
		line(r, false)
	}
}

// StatusBadge colore un statut PASS/FAIL/WARN/SKIP.
func StatusBadge(s string) string {
	switch s {
	case "PASS":
		return Green + s + Reset
	case "FAIL":
		return Red + s + Reset
	case "WARN":
		return Yellow + s + Reset
	default:
		return Dim + s + Reset
	}
}

func stripANSI(s string) string {
	out := strings.Builder{}
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == '\033':
			inEsc = true
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
