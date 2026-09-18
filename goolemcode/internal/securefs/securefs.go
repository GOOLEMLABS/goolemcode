// Package securefs centraliza la creación del estado local del agente con
// permisos restrictivos: directorios 0700 y ficheros 0600.
//
// Ese estado (`.goolem/`) contiene prompts, conversación, tareas y notas que
// pueden incluir datos personales del entorno (IPs privadas, rutas locales,
// usuario) e incluso algún token puntual, así que no debe ser legible por otros
// usuarios de la máquina. En Windows los permisos POSIX no aplican y las
// funciones se comportan como las equivalentes de os.
package securefs

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	// DirMode es el modo de los directorios de estado local del agente.
	DirMode os.FileMode = 0o700
	// FileMode es el modo de los ficheros de estado local del agente.
	FileMode os.FileMode = 0o600
)

// supported indica si la plataforma aplica permisos POSIX.
func supported() bool { return runtime.GOOS != "windows" }

func chmod(path string, mode os.FileMode) error {
	if !supported() {
		return nil
	}
	return os.Chmod(path, mode)
}

// chmodIfNeeded ajusta el modo solo si difiere. Devuelve true si lo cambió.
func chmodIfNeeded(path string, mode os.FileMode) bool {
	if !supported() {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() == mode {
		return false
	}
	return os.Chmod(path, mode) == nil
}

// MkdirAll crea dir (y sus padres) con DirMode y reafirma el modo si el
// directorio ya existía con permisos más laxos.
func MkdirAll(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	return chmod(dir, DirMode)
}

// MkdirAllFor crea el directorio que contiene path.
func MkdirAllFor(path string) error { return MkdirAll(filepath.Dir(path)) }

// WriteFile escribe data en path con FileMode, creando el directorio padre, y
// corrige los permisos de un fichero preexistente más permisivo.
func WriteFile(path string, data []byte) error {
	if err := MkdirAllFor(path); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, FileMode); err != nil {
		return err
	}
	return chmod(path, FileMode)
}

// WriteFileAtomic escribe data de forma atómica (temporal + rename) con FileMode.
func WriteFileAtomic(path string, data []byte) error {
	if err := MkdirAllFor(path); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, FileMode); err != nil {
		return err
	}
	if err := chmod(tmp, FileMode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return chmod(path, FileMode)
}

// OpenAppend abre (creando si hace falta) path en modo append con FileMode.
func OpenAppend(path string) (*os.File, error) {
	if err := MkdirAllFor(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, FileMode)
	if err != nil {
		return nil, err
	}
	if err := chmod(path, FileMode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// HardenTree endurece un árbol de estado local ya existente: DirMode para
// directorios y FileMode para ficheros regulares (migración de instalaciones
// antiguas creadas con 0755/0644). Devuelve cuántas entradas corrigió. Es
// best-effort: ignora enlaces simbólicos y errores.
func HardenTree(root string) int {
	info, err := os.Stat(root)
	if err != nil {
		return 0
	}
	n := 0
	if info.IsDir() && chmodIfNeeded(root, DirMode) {
		n++
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == root || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if chmodIfNeeded(p, DirMode) {
				n++
			}
			return nil
		}
		if d.Type().IsRegular() && chmodIfNeeded(p, FileMode) {
			n++
		}
		return nil
	})
	return n
}
