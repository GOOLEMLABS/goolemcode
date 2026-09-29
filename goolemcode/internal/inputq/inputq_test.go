package inputq

import "testing"

func TestFIFOAndDrain(t *testing.T) {
	q := New()
	q.Push("uno")
	q.Push("dos")
	q.Push("") // vacías se ignoran

	if q.Len() != 2 {
		t.Fatalf("Len=%d, esperado 2", q.Len())
	}
	if got, ok := q.Pop(); !ok || got != "uno" {
		t.Fatalf("Pop=%q,%v; esperado uno,true", got, ok)
	}
	rest := q.Drain()
	if len(rest) != 1 || rest[0] != "dos" {
		t.Fatalf("Drain=%v; esperado [dos]", rest)
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("Pop debía devolver ok=false en cola vacía")
	}
	if got := q.Drain(); got != nil {
		t.Fatalf("Drain de vacía debía ser nil, fue %v", got)
	}
}
