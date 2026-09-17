package projmem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrefersGoolemOverClaude(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("reglas claude"), 0o644)
	os.WriteFile(filepath.Join(dir, "GOOLEM.md"), []byte("reglas goolem"), 0o644)

	got := Load(dir)
	if got == "" || !contains(got, "reglas goolem") || !contains(got, "GOOLEM.md") {
		t.Fatalf("debía cargar GOOLEM.md preferentemente: %q", got)
	}
	if contains(got, "reglas claude") {
		t.Fatal("no debía incluir CLAUDE.md cuando existe GOOLEM.md")
	}
}

func TestLoadFallsBackToClaude(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("reglas claude"), 0o644)
	if got := Load(dir); !contains(got, "reglas claude") {
		t.Fatalf("debía caer a CLAUDE.md: %q", got)
	}
}

func TestLoadNoneReturnsEmpty(t *testing.T) {
	if got := Load(t.TempDir()); got != "" {
		t.Fatalf("sin fichero debía ser vacío: %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
