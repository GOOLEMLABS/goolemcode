package lineedit

import (
	"bufio"
	"strings"
	"testing"
)

// Un pegado en bloque (bracketed paste) debe tratarse como UNA entrada, sin que
// los saltos de línea internos envíen la línea. El buffer muestra una etiqueta
// "[Pasted text #N +M lines]" pero al enviar se expande el contenido real.
func TestEditLineBracketedPaste(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("\x1b[200~linea1\nlinea2\nlinea3\x1b[201~\n"))
	e := New(in, "")

	line, err := e.editLine("> ")
	if err != nil {
		t.Fatalf("editLine: %v", err)
	}
	want := "linea1\nlinea2\nlinea3"
	if line != want {
		t.Fatalf("pegado expandido=%q, esperado %q", line, want)
	}
}

// El marcador de pegado se comporta como una sola unidad al borrar (backspace).
func TestEditLinePasteIsAtomic(t *testing.T) {
	// pega, borra el marcador (1 backspace) y escribe "fin", luego Enter.
	in := bufio.NewReader(strings.NewReader("\x1b[200~a\nb\x1b[201~\x7ffin\n"))
	e := New(in, "")

	line, err := e.editLine("> ")
	if err != nil {
		t.Fatalf("editLine: %v", err)
	}
	if line != "fin" {
		t.Fatalf("linea=%q, esperado \"fin\" (el pegado debía borrarse entero)", line)
	}
}
