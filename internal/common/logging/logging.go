// Package logging — journal structuré fichier (./logs/) + relais console
// pour les erreurs. Chaque exécution crée son propre fichier horodaté.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger écrit des lignes horodatées dans un fichier de log.
// Un Logger nil est utilisable (no-op) pour simplifier les tests.
type Logger struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// New crée logs/ocptk-<timestamp>.log dans dir.
func New(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "ocptk-"+time.Now().Format("20060102-150405")+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Logger{f: f, path: path}, nil
}

func (l *Logger) write(level, format string, a ...any) {
	if l == nil || l.f == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.f, "%s %-5s %s\n", time.Now().Format(time.RFC3339), level, fmt.Sprintf(format, a...))
}

// Info journalise en niveau INFO.
func (l *Logger) Info(format string, a ...any) { l.write("INFO", format, a...) }

// Warn journalise en niveau WARN.
func (l *Logger) Warn(format string, a ...any) { l.write("WARN", format, a...) }

// Error journalise en niveau ERROR.
func (l *Logger) Error(format string, a ...any) { l.write("ERROR", format, a...) }

// Path retourne le chemin du fichier de log courant.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Close ferme le fichier.
func (l *Logger) Close() {
	if l != nil && l.f != nil {
		_ = l.f.Close()
	}
}
