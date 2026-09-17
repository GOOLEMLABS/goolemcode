// Package checkpoint implementa snapshots en memoria estilo Git para /rewind.
// Antes de cada acción mutadora se guarda el contenido previo de los archivos
// relevantes (o nil si no existían); /rewind restaura el último snapshot.
package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type snapshot struct {
	label string
	files map[string]*string // nil = el archivo no existía
}

type Manager struct {
	root  string
	stack []snapshot
	known map[string]bool // working set: rutas leídas o escritas
}

func New(root string) *Manager {
	abs, _ := filepath.Abs(root)
	return &Manager{root: abs, known: map[string]bool{}}
}

// SetRoot reapunta el directorio base (lo usa /cd para seguir al workspace).
func (m *Manager) SetRoot(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		m.root = abs
	}
}

func (m *Manager) safe(rel string) (string, bool) {
	p := filepath.Clean(filepath.Join(m.root, rel))
	if p == m.root || strings.HasPrefix(p, m.root+string(os.PathSeparator)) {
		return p, true
	}
	return "", false
}

func (m *Manager) Track(rel string) {
	if p, ok := m.safe(rel); ok {
		m.known[p] = true
	}
}

// Capture crea un checkpoint. targetRel es el archivo concreto a escribir
// (write_file/edit_file); para comandos/MCP se pasa "" y se fotografía todo el
// working set.
func (m *Manager) Capture(label, targetRel string) {
	paths := map[string]bool{}
	for p := range m.known {
		paths[p] = true
	}
	if targetRel != "" {
		if p, ok := m.safe(targetRel); ok {
			paths[p] = true
		}
	}
	files := map[string]*string{}
	for p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			s := string(data)
			files[p] = &s
		} else {
			files[p] = nil
		}
	}
	m.stack = append(m.stack, snapshot{label: label, files: files})
}

func (m *Manager) Depth() int { return len(m.stack) }

func (m *Manager) Rewind() (string, bool) {
	if len(m.stack) == 0 {
		return "", false
	}
	cp := m.stack[len(m.stack)-1]
	m.stack = m.stack[:len(m.stack)-1]
	var restored []string
	for p, prev := range cp.files {
		rel, _ := filepath.Rel(m.root, p)
		if prev == nil {
			if _, err := os.Stat(p); err == nil {
				_ = os.Remove(p)
				restored = append(restored, "borrado "+rel)
			}
		} else {
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte(*prev), 0o644)
			restored = append(restored, "restaurado "+rel)
		}
	}
	detail := "sin cambios en disco"
	if len(restored) > 0 {
		detail = strings.Join(restored, ", ")
	}
	return fmt.Sprintf("Checkpoint '%s' deshecho: %s", cp.label, detail), true
}

func (m *Manager) Clear() {
	m.stack = nil
	m.known = map[string]bool{}
}
