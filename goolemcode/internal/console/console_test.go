package console

import (
	"strings"
	"testing"
)

func TestCaptureStoresAndTruncates(t *testing.T) {
	c := NewCapture(5)
	if _, err := c.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if got := c.String(); got != "abc" {
		t.Fatalf("String=%q, esperado abc", got)
	}
	if _, err := c.Write([]byte("defgh")); err != nil { // excede el tope
		t.Fatal(err)
	}
	got := c.String()
	if !strings.HasPrefix(got, "abcde") {
		t.Fatalf("debe conservar hasta maxBytes: %q", got)
	}
	if !strings.Contains(got, "truncado") {
		t.Fatalf("debe avisar del truncado: %q", got)
	}
}
