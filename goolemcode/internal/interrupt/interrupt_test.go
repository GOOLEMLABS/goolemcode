package interrupt

import (
	"bufio"
	"os"
	"testing"
	"time"
)

// newTestWatcher crea un Watcher sobre un pipe (sin TTY) para poder probar el
// watchdog sin depender de la terminal real. Devuelve también el extremo de
// escritura para simular pulsaciones de teclas.
func newTestWatcher(t *testing.T) (*Watcher, *os.File) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	w := &Watcher{
		in:        bufio.NewReader(pr),
		fd:        int(pr.Fd()),
		enableRaw: func() (func(), error) { return func() {}, nil },
	}
	return w, pw
}

// startTestWatchdog arranca el bucle del watchdog sin pasar por Start (que
// requiere un TTY real) y devuelve su estado.
func startTestWatchdog(t *testing.T, w *Watcher, cancel func()) *watchdogState {
	t.Helper()
	st := &watchdogState{restore: func() {}, cancel: cancel, waiter: make(chan struct{})}
	if err := st.openStopPipe(); err != nil {
		t.Fatalf("stop pipe: %v", err)
	}
	w.mu.Lock()
	w.wd = st
	w.active = true
	w.mu.Unlock()
	w.runLoop(st)
	return st
}

// Regresión del freeze: Release debe terminar aunque el watchdog esté esperando
// entrada, y NO debe retener w.mu mientras espera. Si lo retuviera, el hilo del
// watchdog (que necesita el candado en isActive para salir) nunca terminaría y
// Release se colgaría, congelando el REPL en la primera confirmación/ask_user.
func TestReleaseDoesNotDeadlock(t *testing.T) {
	w, pw := newTestWatcher(t)
	defer pw.Close()
	startTestWatchdog(t, w, func() {})

	done := make(chan bool, 1)
	go func() { done <- w.Release() }()

	select {
	case ok := <-done:
		if !ok {
			t.Fatal("Release devolvió false con el watchdog activo")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Release se bloqueó: deadlock reteniendo w.mu")
	}
}

// ESC debe cancelar el turno y terminar el watchdog.
func TestWatchdogESPCancels(t *testing.T) {
	w, pw := newTestWatcher(t)
	defer pw.Close()
	canceled := make(chan struct{})
	st := startTestWatchdog(t, w, func() { close(canceled) })

	if _, err := pw.Write([]byte{27}); err != nil { // ESC
		t.Fatalf("write ESC: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("ESC no canceló el turno")
	}
	select {
	case <-st.waiter:
	case <-time.After(2 * time.Second):
		t.Fatal("el watchdog no terminó tras ESC")
	}
}

// Stop debe devolver true si se pulsó ESC y detener el watchdog sin colgarse.
func TestStopReportsEsc(t *testing.T) {
	w, pw := newTestWatcher(t)
	defer pw.Close()
	st := startTestWatchdog(t, w, func() {})
	st.sawEsc = true

	done := make(chan bool, 1)
	go func() { done <- w.Stop() }()
	select {
	case got := <-done:
		if !got {
			t.Fatal("Stop debería informar ESC")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop se bloqueó")
	}
}

// Tras Release + Acquire el watchdog debe volver a funcionar y cancelar con ESC.
func TestReleaseAcquireRestartsWatchdog(t *testing.T) {
	w, pw := newTestWatcher(t)
	defer pw.Close()
	startTestWatchdog(t, w, func() {})

	if !w.Release() {
		t.Fatal("Release devolvió false")
	}
	canceled := make(chan struct{})
	w.Acquire(func() { close(canceled) })

	if _, err := pw.Write([]byte{27}); err != nil {
		t.Fatalf("write ESC: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("el watchdog no se reactivó tras Acquire")
	}
}
