package tools

import (
	"fmt"
	"strings"
)

const (
	// Salida de consola: conservar la cola (donde suelen estar los errores) y
	// acotarla por líneas y bytes.
	cmdMaxLines = 80
	cmdMaxBytes = 12000
	// Captura intermedia: suficiente para calcular cuántas líneas se omiten.
	cmdCaptureBytes = 48000

	// Tope central para CUALQUIER herramienta (incluidas MCP): evita que una
	// salida enorme se reenvíe en cada paso del bucle.
	maxToolResultBytes = 24000
)

// truncateTail conserva las últimas maxLines líneas y como máximo maxBytes del
// final, con un aviso de lo omitido.
func truncateTail(s string, maxLines, maxBytes int) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	omittedLines := 0
	if maxLines > 0 && len(lines) > maxLines {
		omittedLines = len(lines) - maxLines
		lines = lines[len(lines)-maxLines:]
	}
	out := strings.Join(lines, "\n")
	omittedBytes := 0
	if maxBytes > 0 && len(out) > maxBytes {
		omittedBytes = len(out) - maxBytes
		out = out[len(out)-maxBytes:]
	}
	if omittedLines == 0 && omittedBytes == 0 {
		return out
	}
	return fmt.Sprintf("… (output truncated: %d lines / %d bytes omitted; showing the tail)\n%s",
		omittedLines, omittedBytes, out)
}

// truncateMiddle conserva el principio y el final de una salida y omite el
// centro. Es la red de seguridad para herramientas que no acotan su resultado.
func truncateMiddle(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	head := max / 3
	tail := max - head
	return s[:head] + fmt.Sprintf("\n… (%d bytes omitted) …\n", len(s)-max) + s[len(s)-tail:]
}
