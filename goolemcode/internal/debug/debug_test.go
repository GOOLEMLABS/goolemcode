package debug

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogAndDump(t *testing.T) {
	dir := t.TempDir()
	if err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	Logf("hola %d", 42)
	DumpStacks("test")

	data, err := os.ReadFile(filepath.Join(dir, ".goolem", "debug.log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "hola 42") {
		t.Fatalf("falta la línea de log: %q", s)
	}
	if !strings.Contains(s, "GOROUTINE DUMP (test)") {
		t.Fatalf("falta el volcado de goroutines: %q", s)
	}
}
