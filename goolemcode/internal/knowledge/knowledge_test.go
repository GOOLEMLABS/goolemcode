package knowledge

import (
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	// Vacío al principio.
	if s.Index() != "" {
		t.Fatalf("Index() debería ser vacío sin notas, fue: %q", s.Index())
	}

	// Crear nota (sin extensión; debe añadir .md y crear el dir).
	if _, err := s.Write("redes", "# Redes\nEl host GOOLEM está en 192.0.2.10."); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Index incluye el nombre y el resumen (1ª línea sin '#').
	idx := s.Index()
	if !strings.Contains(idx, "redes") || !strings.Contains(idx, "Redes") {
		t.Fatalf("Index() no contiene la nota/resumen: %q", idx)
	}

	// Read devuelve el contenido completo.
	got, err := s.Read("redes")
	if err != nil || !strings.Contains(got, "192.0.2.10") {
		t.Fatalf("Read: %v / %q", err, got)
	}

	// Search encuentra por substring case-insensitive.
	res := s.Search("goolem")
	if !strings.Contains(res, "redes") {
		t.Fatalf("Search no encontró la nota: %q", res)
	}
	if miss := s.Search("inexistente-xyz"); !strings.Contains(miss, "Sin coincidencias") {
		t.Fatalf("Search debería no encontrar nada: %q", miss)
	}

	// path() bloquea escapes del sandbox.
	if _, err := s.path("../../etc/passwd"); err == nil {
		t.Fatal("path() debería rechazar rutas fuera del directorio")
	}
}
