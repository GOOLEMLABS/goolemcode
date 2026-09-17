package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, json string) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(p, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p, func() string { return dir }), dir
}

func TestPreToolBlocksOnNonzeroAndMatches(t *testing.T) {
	r, _ := load(t, `{"hooks":[{"event":"PreToolUse","matcher":"write_file","command":"exit 1"}]}`)
	if block, _ := r.PreTool("write_file", nil); !block {
		t.Fatal("un hook Pre con código != 0 debe bloquear")
	}
	if block, _ := r.PreTool("read_file", nil); block {
		t.Fatal("no debe bloquear si el matcher no casa")
	}
}

func TestPreToolReasonFromOutput(t *testing.T) {
	r, _ := load(t, `{"hooks":[{"event":"PreToolUse","command":"echo motivo-x; exit 2"}]}`)
	block, reason := r.PreTool("cualquiera", nil)
	if !block || reason != "motivo-x" {
		t.Fatalf("motivo esperado 'motivo-x': block=%v reason=%q", block, reason)
	}
}

func TestPreToolReceivesEnv(t *testing.T) {
	// Bloquea solo si GOOLEM_TOOL_NAME es write_file → prueba que la env llega.
	r, _ := load(t, `{"hooks":[{"event":"PreToolUse","command":"[ \"$GOOLEM_TOOL_NAME\" = write_file ] && exit 1 || exit 0"}]}`)
	if block, _ := r.PreTool("write_file", nil); !block {
		t.Fatal("la env GOOLEM_TOOL_NAME no llegó al hook")
	}
	if block, _ := r.PreTool("read_file", nil); block {
		t.Fatal("no debía bloquear para read_file")
	}
}

func TestPostToolRunsInWorkdir(t *testing.T) {
	r, dir := load(t, `{"hooks":[{"event":"PostToolUse","matcher":"write_file","command":"echo hecho > sentinel.txt"}]}`)
	r.PostTool("write_file", map[string]any{"path": "x"}, "resultado", false)
	if _, err := os.Stat(filepath.Join(dir, "sentinel.txt")); err != nil {
		t.Fatalf("el hook Post debía crear sentinel.txt en el workdir: %v", err)
	}
}

func TestLoadCountsAndIgnoresUnknownEvent(t *testing.T) {
	r, _ := load(t, `{"hooks":[
		{"event":"PreToolUse","command":"true"},
		{"event":"PostToolUse","command":"true"},
		{"event":"Otro","command":"true"}
	]}`)
	if r.Count() != 2 {
		t.Fatalf("debía cargar 2 hooks válidos, tiene %d", r.Count())
	}
}

func TestLoadMissingFileEmpty(t *testing.T) {
	r := Load(filepath.Join(t.TempDir(), "noexiste.json"), nil)
	if r.Count() != 0 {
		t.Fatal("fichero inexistente → runner vacío")
	}
}
