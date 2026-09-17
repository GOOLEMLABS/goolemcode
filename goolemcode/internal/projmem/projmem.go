// Package projmem carga las instrucciones del proyecto (al estilo del CLAUDE.md
// de Claude Code): un fichero Markdown en la raíz del proyecto que se inyecta en
// el prompt cada turno, para dar contexto y reglas persistentes al agente.
package projmem

import (
	"os"
	"path/filepath"
	"strings"
)

// candidates son los nombres reconocidos, en orden de preferencia.
var candidates = []string{"GOOLEM.md", "CLAUDE.md", "AGENTS.md"}

const maxBytes = 32 * 1024 // tope para no inundar el contexto

// Load devuelve las instrucciones del proyecto en dir (el primer fichero
// reconocido que exista), o "" si no hay ninguno. Se lee en cada turno, así que
// los cambios en el fichero surten efecto en vivo.
func Load(dir string) string {
	for _, name := range candidates {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		if len(text) > maxBytes {
			text = text[:maxBytes] + "\n… (instrucciones truncadas)"
		}
		return "INSTRUCCIONES DEL PROYECTO (" + name + "):\n" + text
	}
	return ""
}
