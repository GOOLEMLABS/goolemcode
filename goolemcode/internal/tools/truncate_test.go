package tools

import (
	"strings"
	"testing"
)

func TestTruncateTailKeepsEndAndReportsOmissions(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("line\n")
	}
	b.WriteString("ERROR final")
	out := truncateTail(b.String(), 5, 10000)
	if !strings.Contains(out, "ERROR final") {
		t.Fatalf("debía conservar la cola: %q", out)
	}
	if !strings.Contains(out, "omitted") {
		t.Fatalf("debía avisar de líneas omitidas: %q", out)
	}
	if strings.Count(out, "line") > 5 {
		t.Fatalf("debía limitar a 5 líneas: %q", out)
	}
}

func TestTruncateMiddleKeepsHeadAndTail(t *testing.T) {
	s := "HEAD" + strings.Repeat("x", 1000) + "TAIL"
	out := truncateMiddle(s, 100)
	if !strings.HasPrefix(out, "HEAD") || !strings.HasSuffix(out, "TAIL") {
		t.Fatalf("debía conservar cabeza y cola: %q", out)
	}
	if !strings.Contains(out, "omitted") {
		t.Fatalf("debía avisar: %q", out)
	}
}
