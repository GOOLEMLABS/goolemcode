package securefs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("los permisos POSIX no aplican en Windows")
	}
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	return fi.Mode().Perm()
}

func TestWriteFileHardensDirAndFileModes(t *testing.T) {
	skipWindows(t)
	base := filepath.Join(t.TempDir(), ".goolem", "knowledge")
	p := filepath.Join(base, "nota.md")

	if err := WriteFile(p, []byte("hola")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := mode(t, base); got != DirMode {
		t.Errorf("modo del directorio = %o, quiero %o", got, DirMode)
	}
	if got := mode(t, p); got != FileMode {
		t.Errorf("modo del fichero = %o, quiero %o", got, FileMode)
	}
}

func TestWriteFileFixesLooseExistingFile(t *testing.T) {
	skipWindows(t)
	p := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("preparando fichero: %v", err)
	}
	if err := WriteFile(p, []byte("y")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := mode(t, p); got != FileMode {
		t.Errorf("modo del fichero preexistente = %o, quiero %o", got, FileMode)
	}
}

func TestMkdirAllFixesLooseExistingDir(t *testing.T) {
	skipWindows(t)
	dir := filepath.Join(t.TempDir(), ".goolem")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("preparando dir: %v", err)
	}
	if err := MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if got := mode(t, dir); got != DirMode {
		t.Errorf("modo del directorio = %o, quiero %o", got, DirMode)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	skipWindows(t)
	p := filepath.Join(t.TempDir(), ".goolem", "session.json")
	if err := WriteFileAtomic(p, []byte("[]")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if data, err := os.ReadFile(p); err != nil || string(data) != "[]" {
		t.Fatalf("contenido = %q, %v", data, err)
	}
	if got := mode(t, p); got != FileMode {
		t.Errorf("modo = %o, quiero %o", got, FileMode)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("el temporal debería haberse renombrado")
	}
}

func TestOpenAppend(t *testing.T) {
	skipWindows(t)
	p := filepath.Join(t.TempDir(), ".goolem", "history")
	f, err := OpenAppend(p)
	if err != nil {
		t.Fatalf("OpenAppend: %v", err)
	}
	if _, err := f.WriteString("linea\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := mode(t, p); got != FileMode {
		t.Errorf("modo del historial = %o, quiero %o", got, FileMode)
	}
}

func TestHardenTree(t *testing.T) {
	skipWindows(t)
	root := filepath.Join(t.TempDir(), ".goolem")
	sub := filepath.Join(root, "knowledge")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("preparando árbol: %v", err)
	}
	loose := filepath.Join(sub, "nota.md")
	if err := os.WriteFile(loose, []byte("x"), 0o644); err != nil {
		t.Fatalf("preparando fichero: %v", err)
	}

	n := HardenTree(root)
	if n == 0 {
		t.Errorf("HardenTree no corrigió ninguna entrada")
	}
	if got := mode(t, root); got != DirMode {
		t.Errorf("raíz = %o, quiero %o", got, DirMode)
	}
	if got := mode(t, sub); got != DirMode {
		t.Errorf("subdir = %o, quiero %o", got, DirMode)
	}
	if got := mode(t, loose); got != FileMode {
		t.Errorf("fichero = %o, quiero %o", got, FileMode)
	}
	if n := HardenTree(root); n != 0 {
		t.Errorf("segunda pasada corrigió %d entradas, quiero 0", n)
	}
	if n := HardenTree(filepath.Join(root, "no-existe")); n != 0 {
		t.Errorf("árbol inexistente corrigió %d entradas, quiero 0", n)
	}
}
