// Package inputq es una cola FIFO thread-safe de líneas que el usuario escribe
// mientras el agente trabaja. El watchdog del turno empuja aquí cada línea
// (Push); el bucle del agente las consume entre pasos (Drain) y, lo que quede al
// terminar el turno, lo recoge el REPL (Pop).
package inputq

import "sync"

// Queue es una cola de líneas segura para uso concurrente.
type Queue struct {
	mu    sync.Mutex
	lines []string
}

// New crea una cola vacía.
func New() *Queue { return &Queue{} }

// Push añade una línea al final (ignora las vacías).
func (q *Queue) Push(line string) {
	if line == "" {
		return
	}
	q.mu.Lock()
	q.lines = append(q.lines, line)
	q.mu.Unlock()
}

// Pop extrae la primera línea; ok=false si la cola está vacía.
func (q *Queue) Pop() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.lines) == 0 {
		return "", false
	}
	line := q.lines[0]
	q.lines = q.lines[1:]
	return line, true
}

// Drain extrae y devuelve todas las líneas pendientes (nil si no hay).
func (q *Queue) Drain() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.lines) == 0 {
		return nil
	}
	out := q.lines
	q.lines = nil
	return out
}

// Len devuelve cuántas líneas hay pendientes.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.lines)
}
