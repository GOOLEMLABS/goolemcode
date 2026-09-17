// Package diff calcula un diff por líneas (LCS) y lo renderiza coloreado, para
// mostrar los cambios de write_file/edit_file antes de aplicarlos y confirmarlos.
package diff

import (
	"fmt"
	"strings"
)

// Kind es el tipo de una línea en el diff.
type Kind byte

const (
	Equal  Kind = ' '
	Delete Kind = '-'
	Insert Kind = '+'
)

type Line struct {
	Kind Kind
	Text string
}

const maxDiffLines = 4000 // por encima, no calculamos LCS (coste O(n*m))

// Diff devuelve la secuencia de líneas (iguales/borradas/insertadas) entre before
// y after usando la subsecuencia común más larga. Puro → testeable.
func Diff(before, after string) []Line {
	a := splitLines(before)
	b := splitLines(after)
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		// Demasiado grande para LCS: trátalo como reemplazo total.
		out := make([]Line, 0, len(a)+len(b))
		for _, l := range a {
			out = append(out, Line{Delete, l})
		}
		for _, l := range b {
			out = append(out, Line{Insert, l})
		}
		return out
	}

	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var out []Line
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, Line{Equal, a[i]})
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, Line{Delete, a[i]})
			i++
		default:
			out = append(out, Line{Insert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, Line{Delete, a[i]})
	}
	for ; j < m; j++ {
		out = append(out, Line{Insert, b[j]})
	}
	return out
}

// Stat cuenta líneas añadidas y eliminadas.
func Stat(lines []Line) (added, deleted int) {
	for _, l := range lines {
		switch l.Kind {
		case Insert:
			added++
		case Delete:
			deleted++
		}
	}
	return
}

// ANSI
const (
	green = "\x1b[32m"
	red   = "\x1b[31m"
	dim   = "\x1b[2m"
	reset = "\x1b[0m"
)

// Render produce el diff coloreado (con encabezado de estadísticas). maxLines
// limita cuántas líneas se muestran (0 = sin límite).
func Render(before, after string, maxLines int) string {
	lines := Diff(before, after)
	added, deleted := Stat(lines)
	var b strings.Builder
	fmt.Fprintf(&b, "%s+%d %s-%d%s\n", green, added, red, deleted, reset)
	shown := 0
	for _, l := range lines {
		if maxLines > 0 && shown >= maxLines {
			fmt.Fprintf(&b, "%s… (%d líneas más)%s\n", dim, len(lines)-shown, reset)
			break
		}
		switch l.Kind {
		case Insert:
			fmt.Fprintf(&b, "%s+ %s%s\n", green, l.Text, reset)
		case Delete:
			fmt.Fprintf(&b, "%s- %s%s\n", red, l.Text, reset)
		default:
			fmt.Fprintf(&b, "%s  %s%s\n", dim, l.Text, reset)
		}
		shown++
	}
	return strings.TrimRight(b.String(), "\n")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}
