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
// El watchdog NO se bloquea en un read(): usa poll() sobre stdin y sobre un pipe
// de parada. Así Release/Stop pueden despertarlo de forma determinista (y sin
// robarle ninguna tecla al usuario) escribiendo en ese pipe. Restaurar el modo
// del terminal, por sí solo, NO desbloquea un read pendiente (comprobado en
// macOS), de modo que un diseño basado en read()+restore se quedaría colgado.
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

	"github.com/GOOLEMLABS/goolemcode/internal/debug"
	"github.com/GOOLEMLABS/goolemcode/internal/lineedit"
	"golang.org/x/sys/unix"
)

// Watcher coordina el único lector de stdin entre el watchdog del turno y las
// herramientas que leen desde el usuario (ask_user, confirmación).
type Watcher struct {
	mu     sync.Mutex
	in     *bufio.Reader
	wd     *watchdogState
	active bool
	fd     int // fd de stdin vigilado (0 = os.Stdin); inyectable en tests
	// enableRaw activa el modo raw (inyectable en tests, donde no hay TTY).
	enableRaw func() (func(), error)
}

type watchdogState struct {
	restore func()
	cancel  context.CancelFunc
	waiter  chan struct{}
	stopR   *os.File // extremo de lectura del pipe de parada (poll)
	stopW   *os.File // extremo de escritura (Release/Stop lo usan para despertar)
	sawEsc  bool
}

// openStopPipe crea el pipe con el que Release/Stop despiertan al poll.
func (st *watchdogState) openStopPipe() error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	st.stopR, st.stopW = r, w
	return nil
}

// New crea el coordinador sobre el lector de stdin compartido.
func New(in *bufio.Reader) *Watcher { return &Watcher{in: in} }

// stdinFD devuelve el descriptor a vigilar (os.Stdin salvo en tests).
func (w *Watcher) stdinFD() int {
	if w.fd != 0 {
		return w.fd
	}
	return int(os.Stdin.Fd())
}

// rawEnable pone el terminal en modo raw (lineedit.EnableRaw salvo en tests).
func (w *Watcher) rawEnable() (func(), error) {
	if w.enableRaw != nil {
		return w.enableRaw()
	}
	return lineedit.EnableRaw()
}

// Start arranca la vigilia sobre el turno. No-op (false) si stdin no es un
// terminal o no se pudo activar el raw. Mientras está activo, ESC/Ctrl-C llaman
// a cancel (aborta el turno).
func (w *Watcher) Start(cancel context.CancelFunc) bool {
	if !inStdinIsTerminal() {
		return false
	}
	restore, err := w.rawEnable()
	if err != nil {
		return false
	}
	st := &watchdogState{
		restore: restore,
		cancel:  cancel,
		waiter:  make(chan struct{}),
	}
	if err := st.openStopPipe(); err != nil {
		restore()
		return false
	}
	w.mu.Lock()
	w.wd = st
	w.active = true
	w.mu.Unlock()

	w.runLoop(st)
	debug.Logf("watchdog start")
	return true
}

// runLoop es el bucle de vigilancia: espera con poll() a que stdin o el pipe de
// parada tengan datos. No usa read() bloqueante, de modo que Release/Stop pueden
// terminarlo al instante.
func (w *Watcher) runLoop(st *watchdogState) {
	go func() {
		defer close(st.waiter)
		fds := []unix.PollFd{
			{Fd: int32(w.stdinFD()), Events: unix.POLLIN},
			{Fd: int32(st.stopR.Fd()), Events: unix.POLLIN},
		}
		for {
			if !w.isActive(st) {
				return
			}
			// Si bufio ya tiene bytes (lookahead de una lectura anterior),
			// procésalos sin bloquear antes de volver a hacer poll.
			if w.in.Buffered() > 0 {
				if w.handleRune(st) {
					return
				}
				continue
			}
			n, err := unix.Poll(fds, -1)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				return
			}
			if n == 0 {
				continue
			}
			if fds[1].Revents != 0 { // parada solicitada por Release/Stop
				return
			}
			if fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
				if w.handleRune(st) {
					return
				}
			}
		}
	}()
}

// handleRune lee una runa (solo se llama cuando hay datos disponibles) y decide
// si debe detener la vigilia. Devuelve true cuando hay que salir del bucle.
func (w *Watcher) handleRune(st *watchdogState) (stop bool) {
	r, _, err := w.in.ReadRune()
	if err != nil {
		return true
	}
	switch r {
	case 3, 27: // Ctrl-C o ESC → interrumpir el turno
		st.sawEsc = true
		if st.cancel != nil {
			st.cancel()
		}
		return true
	}
	return false
}

func (w *Watcher) isActive(st *watchdogState) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active && w.wd == st
}

// Stop detiene la vigilia al terminar el turno: despierta al watchdog, restaura
// el terminal y espera a que el hilo de lectura termine. Devuelve true si el
// usuario pulsó ESC/Ctrl-C.
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

	w.stopAndWait(st)
	debug.Logf("watchdog stop sawEsc=%v", st.sawEsc)
	return st.sawEsc
}

// Release libera stdin para una lectura de usuario a mitad de turno (ask_user,
// confirmación): detiene el watchdog y restaura el terminal. Debe ir seguida de
// Acquire. No-op si no hay watchdog activo.
func (w *Watcher) Release() bool {
	w.mu.Lock()
	st := w.wd
	if st == nil || !w.active {
		w.mu.Unlock()
		return false
	}
	w.active = false
	w.mu.Unlock()

	debug.Logf("watchdog release (waiting for reader to exit)")
	w.stopAndWait(st)
	debug.Logf("watchdog released")
	return true
}

// stopAndWait despierta al watchdog (escribiendo en su pipe de parada) y espera
// a que su hilo termine. IMPORTANTE: no debe retener w.mu, porque el hilo
// necesita el candado (isActive) para salir; esperarlo con el candado tomado
// provoca un deadlock. Después restaura el terminal al modo previo al raw.
func (w *Watcher) stopAndWait(st *watchdogState) {
	if st.stopW != nil {
		_, _ = st.stopW.Write([]byte{0})
	}
	<-st.waiter
	if st.stopW != nil {
		_ = st.stopW.Close()
	}
	if st.stopR != nil {
		_ = st.stopR.Close()
	}
	st.restore()
}

// Acquire retoma la vigilia tras una lectura de usuario. st ya existe; hay que
// recrear el pipe de parada y relanzar el bucle.
func (w *Watcher) Acquire(cancel context.CancelFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.wd
	if st == nil {
		return
	}
	restore, err := w.rawEnable()
	if err != nil {
		return
	}
	if err := st.openStopPipe(); err != nil {
		restore()
		return
	}
	st.restore = restore
	st.cancel = cancel
	st.waiter = make(chan struct{})
	w.active = true
	w.runLoop(st)
	debug.Logf("watchdog acquire")
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
