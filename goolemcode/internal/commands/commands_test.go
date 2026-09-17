package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLoadsAndGet(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Traducir.md"), []byte("Traduce: $ARGUMENTS"), 0o644)
	os.WriteFile(filepath.Join(dir, "notas.txt"), []byte("ignorar"), 0o644) // no .md

	s := New(dir)
	if names := s.Names(); len(names) != 1 || names[0] != "traducir" {
		t.Fatalf("debía cargar solo 'traducir' (minúsculas): %v", names)
	}
	cmd, ok := s.Get("TRADUCIR")
	if !ok {
		t.Fatal("Get debe ser case-insensitive")
	}
	if cmd.Template != "Traduce: $ARGUMENTS" {
		t.Fatalf("template incorrecta: %q", cmd.Template)
	}
	if _, ok := s.Get("noexiste"); ok {
		t.Fatal("no debía existir")
	}
}

func TestExpandArguments(t *testing.T) {
	if got := Expand("Traduce: $ARGUMENTS", "hola"); got != "Traduce: hola" {
		t.Fatalf("sustitución mal: %q", got)
	}
	// Sin $ARGUMENTS pero con args: se anexan.
	if got := Expand("Resume el proyecto.", "extra"); got != "Resume el proyecto.\n\nextra" {
		t.Fatalf("anexado mal: %q", got)
	}
	// Sin args: plantilla tal cual.
	if got := Expand("Haz X.", ""); got != "Haz X." {
		t.Fatalf("sin args debía dejar la plantilla: %q", got)
	}
}

func TestNewMissingDir(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "noexiste"))
	if len(s.Names()) != 0 {
		t.Fatal("dir inexistente debía dar store vacío")
	}
}

func TestFrontmatterParsing(t *testing.T) {
	content := `---
name: revisar
description: "Revisa el código en busca de problemas"
auto_invoke: true
trigger: revisa
---

Revisa el código actual. Busca bugs, problemas de seguridad y rendimiento.
$ARGUMENTS`
	meta, template := parseFrontmatter(content)
	if meta.Name != "revisar" {
		t.Fatalf("name: esperado revisar, got %q", meta.Name)
	}
	if meta.Description != "Revisa el código en busca de problemas" {
		t.Fatalf("description: %q", meta.Description)
	}
	if !meta.AutoInvoke {
		t.Fatal("auto_invoke debía ser true")
	}
	if meta.Trigger != "revisa" {
		t.Fatalf("trigger: %q", meta.Trigger)
	}
	if !strings.Contains(template, "Revisa el código actual") {
		t.Fatalf("template incorrecta: %q", template)
	}
	if !strings.Contains(template, "$ARGUMENTS") {
		t.Fatal("template debe contener $ARGUMENTS")
	}
}

func TestFrontmatterMinimal(t *testing.T) {
	// Sin frontmatter → Meta vacío, contenido íntegro
	content := "Hola mundo"
	meta, template := parseFrontmatter(content)
	if meta != (Meta{}) {
		t.Fatalf("debía ser Meta vacío: %+v", meta)
	}
	if template != "Hola mundo" {
		t.Fatalf("template: %q", template)
	}
}

func TestFrontmatterNameFallback(t *testing.T) {
	// Sin name en frontmatter → se usa el nombre del archivo
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "testcmd.md"), []byte("---\ndescription: algo\n---\ncontenido"), 0o644)
	s := New(dir)
	cmd := s.cmds["testcmd"]
	if cmd.Meta.Name != "testcmd" {
		t.Fatalf("name fallback: esperado 'testcmd', got %q", cmd.Meta.Name)
	}
	if cmd.Meta.Description != "algo" {
		t.Fatalf("description: %q", cmd.Meta.Description)
	}
}

func TestListWithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "revisar.md"), []byte("---\nname: revisar\ndescription: Revisa código\n---\ncontenido"), 0o644)
	os.WriteFile(filepath.Join(dir, "test.md"), []byte("template simple"), 0o644)

	s := New(dir)
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("esperados 2, got %d", len(list))
	}
	// List debe devolver ordenados
	if list[0].Name != "revisar" || list[0].Description != "Revisa código" {
		t.Fatalf("primer cmd: %+v", list[0])
	}
	if list[1].Name != "test" {
		t.Fatalf("segundo cmd: %+v", list[1])
	}
}

func TestAutoInvokeCommands(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "auto1.md"), []byte("---\nauto_invoke: true\n---\nauto1"), 0o644)
	os.WriteFile(filepath.Join(dir, "auto2.md"), []byte("---\nauto_invoke: true\n---\nauto2"), 0o644)
	os.WriteFile(filepath.Join(dir, "manual.md"), []byte("---\nauto_invoke: false\n---\nmanual"), 0o644)

	s := New(dir)
	auto := s.AutoInvokeCommands()
	if len(auto) != 2 {
		t.Fatalf("esperados 2 auto_invoke, got %d", len(auto))
	}
}
