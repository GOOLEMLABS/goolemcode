// Package knowledge mantiene una base de conocimiento en archivos .md: el agente
// las lista, lee, crea/actualiza y busca. El índice (nombre + resumen de 1 línea)
// se inyecta en el system prompt; el contenido completo se carga bajo demanda
// (divulgación progresiva), de modo que escala sin inundar el contexto.
package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
)

type Store struct {
	dir string
}

func New(dir string) *Store {
	abs, _ := filepath.Abs(dir)
	return &Store{dir: abs}
}

type Note struct {
	Name    string
	Summary string
}

// path normaliza un nombre a una ruta .md dentro del directorio (sandbox).
func (s *Store) path(name string) (string, error) {
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}
	p := filepath.Clean(filepath.Join(s.dir, name))
	if p != s.dir && !strings.HasPrefix(p, s.dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("nombre de nota inválido: %s", name)
	}
	return p, nil
}

func (s *Store) List() []Note {
	var notes []Note
	_ = filepath.WalkDir(s.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(s.dir, p)
		name := strings.TrimSuffix(rel, filepath.Ext(rel))
		notes = append(notes, Note{Name: name, Summary: firstLine(p)})
		return nil
	})
	sort.Slice(notes, func(i, j int) bool { return notes[i].Name < notes[j].Name })
	return notes
}

func firstLine(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "#"))
		if ln != "" {
			if len(ln) > 120 {
				ln = ln[:120] + "…"
			}
			return ln
		}
	}
	return ""
}

// Index devuelve el catálogo para el system prompt ("" si no hay notas).
func (s *Store) Index() string {
	return s.indexWith("PROJECT KNOWLEDGE (.md notes; use knowledge_read to read one entirely):")
}

func (s *Store) indexWith(header string) string {
	notes := s.List()
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(header + "\n")
	for _, n := range notes {
		if n.Summary != "" {
			fmt.Fprintf(&b, "- %s: %s\n", n.Name, n.Summary)
		} else {
			fmt.Fprintf(&b, "- %s\n", n.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// CombinedIndex devuelve una función para el system prompt que lista la memoria
// del proyecto y la global por separado y etiquetadas. Si ambos directorios
// coinciden (p. ej. arrancar desde $HOME), solo muestra una para no duplicar.
func CombinedIndex(project, global *Store) func() string {
	return func() string {
		parts := []string{}
		if pi := project.Index(); pi != "" {
			parts = append(parts, pi)
		}
		if global != nil && global.dir != project.dir {
			if gi := global.indexWith("GLOBAL KNOWLEDGE (cross-project shared memory; to write here pass scope:\"global\"):"); gi != "" {
				parts = append(parts, gi)
			}
		}
		return strings.Join(parts, "\n\n")
	}
}

// pick elige el store según el scope ("global" => global, cualquier otro => proyecto).
func pick(project, global *Store, scope string) *Store {
	if strings.ToLower(strings.TrimSpace(scope)) == "global" && global != nil {
		return global
	}
	return project
}

func (s *Store) Read(name string) (string, error) {
	p, err := s.path(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("no existe la nota: %s", name)
	}
	return string(data), nil
}

func (s *Store) Write(name, content string) (string, error) {
	p, err := s.path(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(s.dir, p)
	return rel, nil
}

func (s *Store) Search(query string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return "Consulta vacía."
	}
	var hits []string
	for _, n := range s.List() {
		content, err := s.Read(n.Name)
		if err != nil {
			continue
		}
		for i, ln := range strings.Split(content, "\n") {
			if strings.Contains(strings.ToLower(ln), q) {
				snippet := strings.TrimSpace(ln)
				if len(snippet) > 160 {
					snippet = snippet[:160] + "…"
				}
				hits = append(hits, fmt.Sprintf("%s:%d: %s", n.Name, i+1, snippet))
				if len(hits) >= 50 {
					return strings.Join(hits, "\n")
				}
			}
		}
	}
	if len(hits) == 0 {
		return "Sin coincidencias para: " + query
	}
	return strings.Join(hits, "\n")
}

// RegisterTools registra knowledge_list/read/write/search. `project` es la
// memoria por proyecto (por defecto); `global` la memoria compartida entre
// proyectos, a la que se accede solo si el modelo pasa scope:"global".
func RegisterTools(reg *tools.Registry, project, global *Store) {
	scopeProp := map[string]string{
		"scope": "Memory scope: \"project\" (default) or \"global\" (shared across projects). Use \"global\" ONLY if the user explicitly requests it.",
	}

	reg.Register(model.ToolDefinition{
		Name:        "knowledge_list",
		Description: "List knowledge notes (.md) with their summaries. Defaults to project scope; pass scope:\"global\" for global memory.",
		InputSchema: schemaObj(scopeProp),
		Mutating:    false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		store := pick(project, global, asString(args["scope"]))
		idx := store.Index()
		if store == global {
			idx = store.indexWith("GLOBAL KNOWLEDGE (cross-project shared memory):")
		}
		if idx == "" {
			return "No knowledge notes yet.", nil
		}
		return idx, nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "knowledge_read",
		Description: "Read the full content of a note by its name. Defaults to project scope; scope:\"global\" for global.",
		InputSchema: schemaObj(merge(map[string]string{"name": "Note name (without .md)"}, scopeProp), "name"),
		Mutating:    false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		return pick(project, global, asString(args["scope"])).Read(asString(args["name"]))
	})

	reg.Register(model.ToolDefinition{
		Name:        "knowledge_write",
		Description: "Create or update a note (.md). Put a 1-line summary at the top. Defaults to project scope; pass scope:\"global\" ONLY if the user requests global/shared memory.",
		InputSchema: schemaObj(merge(map[string]string{
			"name":    "Note name (without .md)",
			"content": "Full Markdown content",
		}, scopeProp), "name", "content"),
		Mutating: false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		store := pick(project, global, asString(args["scope"]))
		rel, err := store.Write(asString(args["name"]), asString(args["content"]))
		if err != nil {
			return "", err
		}
		where := "project"
		if store == global {
			where = "global"
		}
		return fmt.Sprintf("Note saved (%s): %s", where, rel), nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "knowledge_search",
		Description: "Search text (substring, case-insensitive) in notes. Defaults to project scope; scope:\"global\" for global.",
		InputSchema: schemaObj(merge(map[string]string{"query": "Text to search for"}, scopeProp), "query"),
		Mutating:    false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		return pick(project, global, asString(args["scope"])).Search(asString(args["query"])), nil
	})
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func schemaObj(props map[string]string, required ...string) map[string]any {
	p := map[string]any{}
	for k, desc := range props {
		p[k] = map[string]any{"type": "string", "description": desc}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": p, "required": required}
}
