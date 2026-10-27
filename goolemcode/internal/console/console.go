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
	tail     bool
	dropped  int
}

// NewCapture crea un Capture que acumula los PRIMEROS maxBytes.
func NewCapture(max int) *Capture { return &Capture{max: max} }

// NewTailCapture crea un Capture que acumula los ÚLTIMOS maxBytes. Es lo
// adecuado para logs de comandos: los errores relevantes suelen estar al final.
func NewTailCapture(max int) *Capture { return &Capture{max: max, tail: true} }

func (c *Capture) Write(p []byte) (int, error) {
	// En vivo primero (fuera de c.mu, ya que Print toma su propio candado).
	Print(string(p))

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tail {
		data := make([]byte, 0, c.buf.Len()+len(p))
		data = append(data, c.buf.Bytes()...)
		data = append(data, p...)
		if len(data) > c.max {
			c.dropped += len(data) - c.max
			data = data[len(data)-c.max:]
		}
		c.buf.Reset()
		c.buf.Write(data)
		return len(p), nil
	}
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
	if c.tail {
		if c.dropped > 0 {
			s = fmt.Sprintf("… (%d bytes omitted; showing the tail)\n", c.dropped) + s
		}
		return s
	}
	if c.overflow {
		s += "\n… (truncado)"
	}
	return s
}
