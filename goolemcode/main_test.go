package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GOOLEMLABS/goolemcode/internal/provider"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
)

func TestProviderName(t *testing.T) {
	cases := []struct {
		p    provider.Provider
		want string
	}{
		{provider.NewDeepSeek("k", "m"), "deepseek"},
		{provider.NewAnthropic("m", 100, "high"), "claude"},
		{provider.NewOllama("http://localhost:11434", "m"), "ollama"},
	}
	for _, tc := range cases {
		if got := providerName(tc.p); got != tc.want {
			t.Fatalf("providerName(%T)=%q, esperado %q", tc.p, got, tc.want)
		}
	}
}

// Regresión: el menú debe respetar la tecla del usuario y no auto-denegar.
// Antes, el goroutine del timeout competía por el canal y le robaba la tecla;
// además, si no respondías en 15 s, asumía "No".
func TestChoosePermissionReadsChoice(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"una pulsacion 1", "1\n", 1},
		{"always 2", "2\n", 2},
		{"texto no", "no\n", 0},
		{"enter", "\n", 0},
		{"ctrl-c", "\x03\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := bufio.NewReader(strings.NewReader(tc.input))
			if got := choosePermission("write_file", "x", in); got != tc.want {
				t.Fatalf("input %q: se obtuvo %d, se esperaba %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestExpandMentionsInlinesFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notas.txt"), []byte("contenido-secreto"), 0o644)
	ws := tools.NewWorkspace(dir)

	out, inc, _, _ := expandMentions("mira @notas.txt por favor", ws)
	if len(inc) != 1 || inc[0] != "notas.txt" {
		t.Fatalf("debía incluir notas.txt: %v", inc)
	}
	if !strings.Contains(out, "contenido-secreto") || !strings.Contains(out, "mira @notas.txt") {
		t.Fatalf("el mensaje debe conservar el original y añadir el contenido: %q", out)
	}
}

func TestExpandMentionsIgnoresEmailAndMissing(t *testing.T) {
	dir := t.TempDir()
	ws := tools.NewWorkspace(dir)

	// Un email no es una mención (la @ va pegada a texto, no tras espacio/inicio),
	// y un fichero inexistente se deja literal.
	out, inc, _, _ := expandMentions("escribe a user@example.com sobre @noexiste.txt", ws)
	if len(inc) != 0 {
		t.Fatalf("no debía incluir nada: %v", inc)
	}
	if out != "escribe a user@example.com sobre @noexiste.txt" {
		t.Fatalf("el mensaje no debía cambiar: %q", out)
	}
}

func TestCompletePath(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "internal"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "main_test.go"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, ".oculto"), nil, 0o644)
	ws := tools.NewWorkspace(dir)

	// Prefijo con dos candidatas.
	got := completePath(ws, "main")
	if len(got) != 2 || got[0] != "main.go" || got[1] != "main_test.go" {
		t.Fatalf("completar 'main' mal: %v", got)
	}
	// Directorio → añade barra; con @ conserva el prefijo.
	if got := completePath(ws, "@intern"); len(got) != 1 || got[0] != "@internal/" {
		t.Fatalf("completar '@intern' mal: %v", got)
	}
	// Los dotfiles se ocultan salvo que se empiece con punto.
	for _, c := range completePath(ws, "") {
		if c == ".oculto" {
			t.Fatal("no debía sugerir dotfiles sin punto inicial")
		}
	}
	if got := completePath(ws, ".oc"); len(got) != 1 || got[0] != ".oculto" {
		t.Fatalf("con punto inicial sí debía sugerir el dotfile: %v", got)
	}
}

func TestExpandMentionsAttachesImage(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "foto.png"), []byte("\x89PNG\r\n...datos..."), 0o644)
	ws := tools.NewWorkspace(dir)

	out, inc, imgNames, images := expandMentions("describe @foto.png", ws)
	if len(inc) != 0 {
		t.Fatalf("una imagen no debe inyectarse como texto: %v", inc)
	}
	if len(images) != 1 || images[0].Media != "image/png" {
		t.Fatalf("debía adjuntar 1 imagen png: %+v", images)
	}
	if len(imgNames) != 1 || imgNames[0] != "foto.png" {
		t.Fatalf("nombre de imagen mal: %v", imgNames)
	}
	if strings.Contains(out, "Contenido de") {
		t.Fatal("no debía inyectar el binario como texto")
	}
}

func TestExpandMentionsRejectsSandboxEscape(t *testing.T) {
	dir := t.TempDir()
	ws := tools.NewWorkspace(dir)
	out, inc, _, _ := expandMentions("lee @../../etc/passwd", ws)
	if len(inc) != 0 || out != "lee @../../etc/passwd" {
		t.Fatalf("no debía resolver fuera del sandbox: inc=%v out=%q", inc, out)
	}
}
