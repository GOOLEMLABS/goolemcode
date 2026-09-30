// Package console serializa las escrituras a stdout. El eco de lo que el usuario
// teclea (desde el watchdog) y el streaming del agente corren en goroutines
// distintas; sin un candado común, sus líneas se entremezclarían y corromperían
// la pantalla. Todas las salidas interactivas pasan por aquí.
package console

import (
	"bytes"
	"fmt"
	"sync"
)

var mu sync.Mutex

// Print escribe sin formato (equivalente a fmt.Print) bajo el candado de salida.
func Print(a ...any) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Print(a...)
}

// Printf escribe con formato (equivalente a fmt.Printf) bajo el candado.
func Printf(format string, a ...any) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Printf(format, a...)
}

// Println escribe una línea (equivalente a fmt.Println) bajo el candado.
func Println(a ...any) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Println(a...)
}

// Capture es un io.Writer que reenvía lo escrito a stdout EN VIVO (bajo el
// candado de salida, para no entremezclarse con el eco ni el streaming del
// modelo) y además lo acumula hasta maxBytes para devolverlo como resultado de
// la herramienta. Es seguro para escrituras concurrentes (stdout y stderr).
type Capture struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	max      int
	overflow bool
}

// NewCapture crea un Capture que acumula como máximo maxBytes.
func NewCapture(max int) *Capture { return &Capture{max: max} }

func (c *Capture) Write(p []byte) (int, error) {
	// En vivo primero (fuera de c.mu, ya que Print toma su propio candado).
	Print(string(p))

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.overflow {
		if room := c.max - c.buf.Len(); room <= 0 {
			c.overflow = true
		} else if len(p) > room {
			c.buf.Write(p[:room])
			c.overflow = true
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

// String devuelve lo acumulado (con aviso de truncado si excedió maxBytes).
func (c *Capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.buf.String()
	if c.overflow {
		s += "\n… (truncado)"
	}
	return s
}
