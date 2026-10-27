package model

import "fmt"

// ShrinkStrings recorre un valor JSON-like (map[string]any, []any, string) y
// sustituye las cadenas mayores que max por un marcador con su tamaño. Sirve
// para conservar la estructura de los argumentos de una tool_call (nombre, path,
// ids…) sin reenviar en cada iteración contenidos enormes como el "content" de
// write_file o el "old_string" de edit_file.
//
// Devuelve el valor (posiblemente el mismo map/slice mutado) y si hubo cambios.
func ShrinkStrings(v any, max int) (any, bool) {
	if max <= 0 {
		return v, false
	}
	switch x := v.(type) {
	case string:
		if len(x) > max {
			return fmt.Sprintf("[%d bytes omitted]", len(x)), true
		}
		return x, false
	case map[string]any:
		changed := false
		for k, val := range x {
			nv, ch := ShrinkStrings(val, max)
			if ch {
				x[k] = nv
				changed = true
			}
		}
		return x, changed
	case []any:
		changed := false
		for i, val := range x {
			nv, ch := ShrinkStrings(val, max)
			if ch {
				x[i] = nv
				changed = true
			}
		}
		return x, changed
	}
	return v, false
}
