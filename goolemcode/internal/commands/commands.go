// Package commands carga comandos slash personalizados desde
// <workdir>/.goolem/commands/*.md (al estilo de los slash commands de Claude
// Code). Cada fichero "foo.md" define /foo; su contenido es una plantilla de
// prompt donde $ARGUMENTS se sustituye por lo que el usuario escriba tras /foo.
//
// Frontmatter YAML opcional entre ---:
//
//	---
//	name: foo
//	description: "Descripción amigable"
//	auto_invoke: true   # opcional: el agente lo llama automáticamente
//	trigger: "palabras" # opcional: palabras que disparan auto-invocación
//	---
//
// El contenido tras el frontmatter es la plantilla.
package commands

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Meta describe los metadatos de un comando (frontmatter YAML).
type Meta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	AutoInvoke  bool   `yaml:"auto_invoke"`
	Trigger     string `yaml:"trigger"`
}

// Cmd representa un comando cargado con sus metadatos y plantilla.
type Cmd struct {
	Meta
	Template string // plantilla del prompt (sin frontmatter)
}

// Store mantiene los comandos cargados desde el directorio .goolem/commands.
type Store struct {
	cmds map[string]*Cmd // nombre → Cmd (nombre en minúsculas)
}

// New carga los .md de dir (si no existe, queda vacío). Procesa frontmatter YAML
// entre --- (si no hay, Meta queda vacío y todo el contenido es plantilla).
func New(dir string) *Store {
	s := &Store{cmds: map[string]*Cmd{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return s
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		raw := string(data)
		name := strings.ToLower(strings.TrimSuffix(e.Name(), ".md"))
		meta, template := parseFrontmatter(raw)
		if meta.Name == "" {
			meta.Name = name
		}
		s.cmds[name] = &Cmd{Meta: meta, Template: strings.TrimRight(template, "\n")}
	}
	return s
}

// parseFrontmatter separa el frontmatter YAML (entre ---) del contenido.
// Si no hay frontmatter válido, devuelve Meta vacío y el contenido íntegro.
func parseFrontmatter(raw string) (Meta, string) {
	raw = strings.TrimLeft(raw, "\n")
	if !strings.HasPrefix(raw, "---\n") && !strings.HasPrefix(raw, "---\r\n") {
		return Meta{}, raw
	}
	rest := raw[4:] // salta "---\n"
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Meta{}, raw
	}
	fm := rest[:end]
	meta := parseYAMLSimple(fm)
	content := rest[end+5:] // salta "\n---\n" (o "\n---\r\n")
	return meta, content
}

// parseYAMLSimple parsea campos YAML planos (clave: "valor" o clave: valor) sin
// anidamiento. Suficiente para frontmatter básico.
func parseYAMLSimple(s string) Meta {
	m := Meta{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		val = strings.Trim(val, `"`)

		switch key {
		case "name":
			m.Name = strings.ToLower(val)
		case "description":
			m.Description = val
		case "auto_invoke":
			m.AutoInvoke = val == "true" || val == "yes" || val == "1"
		case "trigger":
			m.Trigger = val
		}
	}
	return m
}

// Get devuelve el comando (y si existe).
func (s *Store) Get(name string) (*Cmd, bool) {
	cmd, ok := s.cmds[strings.ToLower(name)]
	return cmd, ok
}

// Names lista los comandos disponibles, ordenados.
func (s *Store) Names() []string {
	names := make([]string, 0, len(s.cmds))
	for n := range s.cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// List devuelve la información detallada de todos los comandos (nombre + descripción).
func (s *Store) List() []Cmd {
	out := make([]Cmd, 0, len(s.cmds))
	for _, cmd := range s.cmds {
		out = append(out, *cmd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AutoInvokeCommands devuelve los comandos con auto_invoke = true.
func (s *Store) AutoInvokeCommands() []Cmd {
	var out []Cmd
	for _, cmd := range s.cmds {
		if cmd.AutoInvoke {
			out = append(out, *cmd)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Expand sustituye $ARGUMENTS por args en la plantilla. Si no hay $ARGUMENTS y se
// pasaron args, se anexan al final.
func Expand(template, args string) string {
	if strings.Contains(template, "$ARGUMENTS") {
		return strings.ReplaceAll(template, "$ARGUMENTS", args)
	}
	if strings.TrimSpace(args) != "" {
		return template + "\n\n" + args
	}
	return template
}
