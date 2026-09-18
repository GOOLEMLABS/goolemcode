// Package lineedit da al REPL un historial navegable con las flechas ↑/↓ y
// edición básica de línea (←/→, backspace, Home/End), SIN dependencias externas:
// pone el terminal en modo raw vía `stty` (presente en macOS/Linux) y lee byte a
// byte. Si stdin no es un terminal (entrada por tubería) o `stty` no está,
// degrada a lectura de línea normal — así los tests con pipes siguen funcionando.
package lineedit

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/GOOLEMLABS/goolemcode/internal/securefs"
	"golang.org/x/term"
)

var (
	// ErrInterrupted lo devuelve ReadLine cuando el usuario pulsa Ctrl-C (para que el
	// REPL descarte la línea y muestre un prompt nuevo, en vez de salir).
	ErrInterrupted = errors.New("interrumpido")
	// ErrEscPressed se devuelve cuando se pulsa ESC en el prompt vacío para
	// interrumpir la ejecución del agente.
	ErrEscPressed = errors.New("escape pulsado")
	// errEOF para Ctrl-D en línea vacía (interno de editLine).
	errEOF = errors.New("EOF")
)

const maxHistory = 1000

// Editor mantiene el historial y comparte el lector de stdin con el resto del
// REPL (las lecturas son secuenciales, nunca concurrentes).
type Editor struct {
	in       *bufio.Reader
	history  []string
	histFile string
	pos      int // índice de navegación en el historial
	// complete devuelve los reemplazos completos para la palabra actual (Tab).
	complete  func(word string) []string
	lastLines int // líneas físicas que ocupó el último redraw (para limpiar multilínea)
}

// SetCompleter registra el autocompletado (Tab). El completador recibe la palabra
// bajo el cursor y devuelve las candidatas completas que la reemplazan.
func (e *Editor) SetCompleter(fn func(word string) []string) { e.complete = fn }

// New crea el editor sobre el lector compartido y carga el historial de histFile.
func New(in *bufio.Reader, histFile string) *Editor {
	e := &Editor{in: in, histFile: histFile}
	e.loadHistory()
	e.pos = len(e.history)
	return e
}

// ReadLine muestra prompt y devuelve la línea introducida. Con un terminal real
// habilita historial y edición; si no, hace una lectura de línea simple.
// Si se pulsa ESC en el prompt vacío, devuelve ErrEscPressed.
func (e *Editor) ReadLine(prompt string) (string, error) {
	if !isTerminal() {
		fmt.Print(prompt)
		line, err := e.in.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	restore, err := enableRaw()
	if err != nil {
		// Sin raw no hay flechas; cae a lectura simple.
		fmt.Print(prompt)
		line, err := e.in.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	defer restore()
	return e.editLine(prompt)
}

// editLine es el bucle del editor en modo raw.
func (e *Editor) editLine(prompt string) (string, error) {
	var buf []rune
	cursor := 0
	e.pos = len(e.history)
	e.redraw(prompt, buf, cursor)

	for {
		r, _, err := e.in.ReadRune()
		if err != nil {
			return "", err
		}
		switch r {
		case '\r', '\n': // Enter
			fmt.Print("\r\n")
			line := string(buf)
			e.addHistory(line)
			return line, nil
		case 3: // Ctrl-C
			fmt.Print("\r\n")
			return "", ErrInterrupted
		case 4: // Ctrl-D
			if len(buf) == 0 {
				fmt.Print("\r\n")
				return "", errEOF
			}
		case 127, 8: // Backspace
			if cursor > 0 {
				buf = append(buf[:cursor-1], buf[cursor:]...)
				cursor--
				e.redraw(prompt, buf, cursor)
			}
		case 9: // Tab → autocompletar la palabra bajo el cursor
			if e.complete == nil {
				break
			}
			start := wordStart(buf, cursor)
			word := string(buf[start:cursor])
			comps := e.complete(word)
			if len(comps) == 0 {
				break
			}
			repl := comps[0]
			if len(comps) > 1 {
				repl = longestCommonPrefix(comps)
				if repl == word { // sin progreso: muestra las candidatas
					fmt.Print("\r\n" + strings.Join(baseNames(comps), "  ") + "\r\n")
					e.redraw(prompt, buf, cursor)
					break
				}
			}
			tail := append([]rune{}, buf[cursor:]...)
			buf = append(buf[:start], []rune(repl)...)
			cursor = len(buf)
			buf = append(buf, tail...)
			e.redraw(prompt, buf, cursor)
		case 1: // Ctrl-A → inicio
			cursor = 0
			e.redraw(prompt, buf, cursor)
		case 5: // Ctrl-E → fin
			cursor = len(buf)
			e.redraw(prompt, buf, cursor)
		case 27: // ESC: inicio de secuencia (flechas) o ESC suelta
			// Las flechas envían "ESC [ X" de golpe: si tras la ESC no hay bytes
			// en el buffer, fue una ESC suelta.
			if e.in.Buffered() == 0 {
				if len(buf) == 0 {
					// ESC en prompt vacío → interrumpir agente
					fmt.Print("\r\n")
					return "", ErrEscPressed
				}
				// Con texto en línea: limpiarla
				buf = buf[:0]
				cursor = 0
				e.redraw(prompt, buf, cursor)
				break
			}
			r2, _, err := e.in.ReadRune()
			if err != nil {
				return "", err
			}
			if r2 != '[' && r2 != 'O' {
				continue
			}
			r3, _, err := e.in.ReadRune()
			if err != nil {
				return "", err
			}
			switch r3 {
			case 'A': // ↑ historial anterior
				if s, ok := e.historyPrev(); ok {
					buf = []rune(s)
					cursor = len(buf)
					e.redraw(prompt, buf, cursor)
				}
			case 'B': // ↓ historial siguiente
				if s, ok := e.historyNext(); ok {
					buf = []rune(s)
					cursor = len(buf)
					e.redraw(prompt, buf, cursor)
				}
			case 'C': // → derecha
				if cursor < len(buf) {
					cursor++
					e.redraw(prompt, buf, cursor)
				}
			case 'D': // ← izquierda
				if cursor > 0 {
					cursor--
					e.redraw(prompt, buf, cursor)
				}
			case 'H': // Home
				cursor = 0
				e.redraw(prompt, buf, cursor)
			case 'F': // End
				cursor = len(buf)
				e.redraw(prompt, buf, cursor)
			case '3': // Supr: ESC [ 3 ~
				if r4, _, _ := e.in.ReadRune(); r4 == '~' && cursor < len(buf) {
					buf = append(buf[:cursor], buf[cursor+1:]...)
					e.redraw(prompt, buf, cursor)
				}
			}
		default:
			if r >= 32 { // imprimible
				buf = append(buf, 0)
				copy(buf[cursor+1:], buf[cursor:])
				buf[cursor] = r
				cursor++
				e.redraw(prompt, buf, cursor)
			}
		}
	}
}

// redraw repinta la línea completa manejando texto multilínea: sube hasta la
// primera línea del prompt, borra todo, redibuja y recoloca el cursor.
func (e *Editor) redraw(prompt string, buf []rune, cursor int) {
	w := termWidth()
	pr := len([]rune(prompt)) // prompt visual (sin ANSI; nunca lleva escapes en este REPL)

	// Subir hasta el inicio del bloque dibujado anteriormente
	if e.lastLines > 1 {
		fmt.Printf("\x1b[%dA", e.lastLines-1)
	}
	fmt.Print("\r\x1b[0J") // columna 0, borrar hasta el final de la pantalla

	// Dibujar prompt + buffer
	fmt.Print(prompt)
	fmt.Print(string(buf))

	// Calcular cuántas líneas físicas ocupa ahora
	total := pr + len(buf)
	if total == 0 {
		e.lastLines = 1
	} else {
		e.lastLines = (total + w - 1) / w
	}

	// Posicionar el cursor dentro del texto
	cursorPos := pr + cursor        // columna visual absoluta (en runas)
	tgtLine := cursorPos / w        // línea destino (0 = primera)
	tgtCol := cursorPos % w         // columna dentro de esa línea
	up := e.lastLines - 1 - tgtLine // líneas que subir desde el final
	if up > 0 {
		fmt.Printf("\x1b[%dA", up)
	}
	fmt.Printf("\r\x1b[%dC", tgtCol)
}

// termWidth devuelve el ancho del terminal en columnas, o 80 si falla.
func termWidth() int {
	if w, _, err := term.GetSize(int(os.Stdin.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// ReadKey lee una sola tecla en modo raw (sin esperar Enter) y devuelve su runa.
// Si stdin no es un terminal (tubería/archivo), lee una línea y devuelve el primer
// carácter, o '\n' si está vacía. Es ideal para menús sí/no sin pulsar Enter.
func ReadKey(in *bufio.Reader) (rune, error) {
	if !isTerminal() {
		line, err := in.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return '\n', nil
		}
		return rune(line[0]), nil
	}
	restore, err := enableRaw()
	if err != nil {
		// fallback: sin raw, lee línea completa
		line, err := in.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return '\n', nil
		}
		return rune(line[0]), nil
	}
	defer restore()
	r, _, err := in.ReadRune()
	if err != nil {
		return 0, err
	}
	return r, nil
}

// IsEOF indica si el error corresponde a fin de entrada (Ctrl-D o pipe cerrada).
func IsEOF(err error) bool {
	return err == errEOF || (err != nil && err.Error() == "EOF")
}

func (e *Editor) historyPrev() (string, bool) {
	if e.pos > 0 {
		e.pos--
		return e.history[e.pos], true
	}
	return "", false
}

func (e *Editor) historyNext() (string, bool) {
	if e.pos < len(e.history)-1 {
		e.pos++
		return e.history[e.pos], true
	}
	if e.pos == len(e.history)-1 {
		e.pos++
		return "", true // más allá del último → línea vacía
	}
	return "", false
}

func (e *Editor) addHistory(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if n := len(e.history); n > 0 && e.history[n-1] == line {
		e.pos = len(e.history)
		return // no dupliques la anterior consecutiva
	}
	e.history = append(e.history, line)
	if len(e.history) > maxHistory {
		e.history = e.history[len(e.history)-maxHistory:]
	}
	e.pos = len(e.history)
	e.appendHistoryFile(line)
}

func (e *Editor) loadHistory() {
	if e.histFile == "" {
		return
	}
	data, err := os.ReadFile(e.histFile)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			e.history = append(e.history, line)
		}
	}
	if len(e.history) > maxHistory {
		e.history = e.history[len(e.history)-maxHistory:]
	}
}

func (e *Editor) appendHistoryFile(line string) {
	if e.histFile == "" {
		return
	}
	f, err := securefs.OpenAppend(e.histFile)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// isTerminal indica si stdin es un terminal (no una tubería/archivo).
func isTerminal() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

// EnableRaw pone el terminal en modo raw vía `stty` y devuelve una función para
// restaurar los ajustes originales. Desactiva echo, canónico y señales (así
// Ctrl-C/Ctrl-D llegan como bytes que gestiona el editor); OPOST sigue activo,
// de modo que los saltos de línea de salida se traducen con normalidad.
func EnableRaw() (func(), error) {
	old, err := SttyGet()
	if err != nil {
		return nil, err
	}
	if err := SttySet("-echo", "-icanon", "-isig", "min", "1", "time", "0"); err != nil {
		return nil, err
	}
	return func() { _ = SttyRestore(old) }, nil
}

// SttyGet captura la configuración actual de la terminal (stty -g).
func SttyGet() (string, error) {
	cmd := exec.Command("stty", "-g")
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// SttySet ejecuta stty con los argumentos dados.
func SttySet(args ...string) error {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// SttyRestore restaura la configuración guardada de la terminal.
func SttyRestore(saved string) error {
	if saved == "" {
		return SttySet("sane")
	}
	return SttySet(saved)
}

// enableRaw llama a EnableRaw (mantiene compatibilidad interna).
func enableRaw() (func(), error) { return EnableRaw() }

// wordStart devuelve el índice donde empieza la palabra que termina en cursor
// (retrocede hasta un espacio o el inicio).
func wordStart(buf []rune, cursor int) int {
	i := cursor
	for i > 0 && buf[i-1] != ' ' && buf[i-1] != '\t' {
		i--
	}
	return i
}

// longestCommonPrefix devuelve el prefijo común más largo de las cadenas.
func longestCommonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}

// baseNames devuelve el último segmento de cada ruta (para listar candidatas).
func baseNames(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		t := strings.TrimSuffix(s, "/")
		if idx := strings.LastIndexByte(t, '/'); idx >= 0 {
			out[i] = t[idx+1:]
		} else {
			out[i] = t
		}
		if strings.HasSuffix(s, "/") {
			out[i] += "/"
		}
	}
	return out
}
