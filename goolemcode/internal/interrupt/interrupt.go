// Package interrupt ofrece un watchdog en segundo plano que, mientras un turno
// está en ejecución, pone el terminal en modo raw y vigila stdin buscando ESC
// (o Ctrl-C) para que el usuario pueda interrumpir la ejecución aunque el agente
// esté bloqueado esperando a un proveedor lento o con fallos.
//
// Problema que resuelve: en una sesión normal, ESC se procesa solo dentro del
// prompt (lineedit). Si el agente está "atascado" reintentando contra un Ollama
// inaccesible (sin VPN), el bucle de turno está ocupado y nadie lee stdin, así
// que ESC no tenía forma de detenerlo. Este watchdog es el único lector de stdin
// mientras el turno corre y, al pulsar ESC/Ctrl-C, cancela el contexto (lo que
// aborta las llamadas HTTP/SSE en curso).
//
// Coordinación de stdin: hay exactamente UN lector a la vez.
//   - Durante un turno: el watchdog (modo raw) es el único lector.
//   - Cuando el agente invoca ask_user o pide confirmación a mitad de turno, la
//     herramienta hace `Release()` (restaura el terminal y espera a que el
//     watchdog deje de leer) y entonces usa stdin con lectura de línea normal.
//     Al terminar hace `Acquire()` para retomar la vigilia.
//
// Stdin siempre vuelve a modo canónico al terminar el watchdog, así el prompt
// siguiente (lineedit) lo reutiliza con normalidad.
package interrupt

import (
	"bufio"
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/GOOLEMLABS/goolemcode/internal/lineedit"
)

// Watcher coordina el único lector de stdin entre el watchdog del turno y las
// herramientas que leen desde el usuario (ask_user, confirmación).
type Watcher struct {
	mu     sync.Mutex
	in     *bufio.Reader
	wd     *watchdogState
	active bool
}

type watchdogState struct {
	restore  func()
	cancel   context.CancelFunc
	waiter   chan struct{}
	pauseCh  chan struct{}
	resumeCh chan struct{}
	sawEsc   bool
}

// New crea el coordinador sobre el lector de stdin compartido.
func New(in *bufio.Reader) *Watcher { return &Watcher{in: in} }

// Start arranca la vigilia sobre el turno. No-op (false) si stdin no es un
// terminal o no se pudo activar el raw. Mientras está activo, ESC/Ctrl-C llaman
// a cancel (aborta el turno).
func (w *Watcher) Start(cancel context.CancelFunc) bool {
	if !inStdinIsTerminal() {
		return false
	}
	restore, err := lineedit.EnableRaw()
	if err != nil {
		return false
	}
	st := &watchdogState{
		restore:  restore,
		cancel:   cancel,
		waiter:   make(chan struct{}),
		pauseCh:  make(chan struct{}),
		resumeCh: make(chan struct{}),
	}
	w.mu.Lock()
	w.wd = st
	w.active = true
	w.mu.Unlock()

	w.runLoop(st)
	return true
}

// runLoop es el bucle de lectura del teclado en modo raw.
func (w *Watcher) runLoop(st *watchdogState) {
	go func() {
		defer close(st.waiter)
		for {
			if !w.isActive(st) {
				return
			}
			r, _, err := w.in.ReadRune()
			if err != nil {
				return
			}
			switch r {
			case 3, 27: // Ctrl-C o ESC → interrumpir el turno
				st.sawEsc = true
				if st.cancel != nil {
					st.cancel()
				}
				// seguimos viviendo un momento para que Stop libere stdin limpiamente
				return
			}
		}
	}()
}

func (w *Watcher) isActive(st *watchdogState) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active && w.wd == st
}

// Stop detiene la vigilia al terminar el turno: restaura el terminal y espera a
// que el hilo de lectura termine. Devuelve true si el usuario pulsó ESC/Ctrl-C.
func (w *Watcher) Stop() bool {
	w.mu.Lock()
	if !w.active || w.wd == nil {
		w.mu.Unlock()
		return false
	}
	st := w.wd
	w.wd = nil
	w.active = false
	w.mu.Unlock()

	// Poner el terminal en modo canónico ANTES de esperar, para que el hilo de
	// lectura (bloqueado en ReadRune) reciba el ajuste y, si estaba bloqueado,
	// pueda despertarse o al menos liberar el fd.
	st.restore()
	<-st.waiter
	return st.sawEsc
}

// Release libera stdin para una lectura de usuario a mitad de turno (ask_user,
// confirmación): detiene el watchdog y restaura el terminal. Debe ir seguida de
// Acquire. No-op si no hay watchdog activo.
func (w *Watcher) Release() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.wd
	if st == nil || !w.active {
		return false
	}
	w.active = false
	st.restore()
	<-st.waiter
	return true
}

// Acquire retoma la vigilia tras una lectura de usuario. st ya existe; hay que
// restaurar canales y relanzar el bucle.
func (w *Watcher) Acquire(cancel context.CancelFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.wd
	if st == nil {
		return
	}
	restore, err := lineedit.EnableRaw()
	if err != nil {
		return
	}
	st.restore = restore
	st.cancel = cancel
	st.waiter = make(chan struct{})
	w.active = true
	w.runLoop(st)
}

var inStdinIsTerminal = func() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

// SignalCh instala el manejador de SIGINT (Ctrl-C de la terminal) como refuerzo
// en modos degradados. Devuelve el canal y un stop para signal.Stop.
func SignalCh() (chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return ch, func() { signal.Stop(ch) }
}

var _ = context.Background
