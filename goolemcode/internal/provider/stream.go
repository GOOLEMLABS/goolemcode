package provider

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// StreamIdleTimeout es el tiempo máximo sin recibir datos de un stream de
// proveedor antes de abortarlo. Evita que una conexión estancada (el servidor
// mantiene la conexión abierta pero no envía nada) bloquee el turno hasta el
// timeout total del cliente HTTP (10 min). Se puede ajustar desde la config
// (stream_idle_timeout_seconds).
var StreamIdleTimeout = 120 * time.Second

// stallGuard envuelve un io.Reader y cierra el body si pasa `timeout` sin
// recibir datos. La lectura pendiente se desbloquea con un error descriptivo.
// Cada lectura con datos reinicia el temporizador.
type stallGuard struct {
	r       io.Reader
	closer  func() error
	timeout time.Duration

	mu    sync.Mutex
	timer *time.Timer
	fired bool
}

func newStallGuard(r io.Reader, closer func() error, timeout time.Duration) *stallGuard {
	g := &stallGuard{r: r, closer: closer, timeout: timeout}
	g.arm()
	return g
}

// arm (re)inicia el temporizador de inactividad.
func (g *stallGuard) arm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(g.timeout, func() {
		g.mu.Lock()
		g.fired = true
		g.mu.Unlock()
		_ = g.closer() // cierra el body → desbloquea la lectura pendiente
	})
}

func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	if n > 0 {
		g.arm() // llegaron datos: reinicia la cuenta
	}
	if err != nil {
		g.mu.Lock()
		fired := g.fired
		g.mu.Unlock()
		if fired {
			return n, fmt.Errorf("provider stream stalled: no data for %s", g.timeout)
		}
	}
	return n, err
}

// stop cancela el temporizador (llamar al terminar de leer).
func (g *stallGuard) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
	}
}
