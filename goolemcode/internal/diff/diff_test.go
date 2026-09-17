package diff

import "testing"

func TestDiffBasic(t *testing.T) {
	before := "uno\ndos\ntres"
	after := "uno\nDOS\ntres\ncuatro"
	lines := Diff(before, after)
	added, deleted := Stat(lines)
	if added != 2 || deleted != 1 { // +DOS +cuatro, -dos
		t.Fatalf("stat esperado +2 -1, obtenido +%d -%d", added, deleted)
	}
	// La primera y la línea "tres" deben conservarse como iguales.
	var equals int
	for _, l := range lines {
		if l.Kind == Equal {
			equals++
		}
	}
	if equals != 2 { // uno, tres
		t.Fatalf("esperadas 2 líneas iguales, %d", equals)
	}
}

func TestDiffCreateAndDelete(t *testing.T) {
	if _, del := Stat(Diff("", "a\nb")); del != 0 {
		t.Fatalf("crear no debe borrar líneas")
	}
	if add, _ := Stat(Diff("", "a\nb")); add != 2 {
		t.Fatalf("crear debe añadir 2 líneas")
	}
	if add, _ := Stat(Diff("a\nb", "")); add != 0 {
		t.Fatalf("vaciar no debe añadir")
	}
}

func TestDiffIdentical(t *testing.T) {
	add, del := Stat(Diff("igual\ntexto", "igual\ntexto"))
	if add != 0 || del != 0 {
		t.Fatalf("idéntico no debe tener cambios: +%d -%d", add, del)
	}
}
