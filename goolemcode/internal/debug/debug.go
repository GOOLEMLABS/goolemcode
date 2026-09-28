// Package debug ofrece un modo de depuración opcional (`-debug`) que registra
// hitos del REPL en <workdir>/.goolem/debug.log y permite volcar todas las pilas
// de goroutines con SIGUSR1. Sirve para diagnosticar cuelgues sin depender de
// herramientas externas (sample, lldb): si se congela, miras el log y, si hace
// falta, mandas `kill -USR1 <pid>` para volcar las goroutines.
//
// Cuando está desactivado, Logf/DumpStacks son no-ops baratos.
package debug

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/securefs"
)

var (
	mu      sync.Mutex
	file    *os.File
	enabled bool
)

// Enable abre el log de depuración en <dir>/.goolem/debug.log y activa el
// volcado de goroutines con SIGUSR1. Es idempotente.
func Enable(dir string) error {
	mu.Lock()
	defer mu.Unlock()
	if enabled {
		return nil
	}
	path := filepath.Join(dir, ".goolem", "debug.log")
	f, err := securefs.OpenAppend(path)
	if err != nil {
		return err
	}
	file = f
	enabled = true
	fmt.Fprintf(f, "\n===== goolemcode debug =====\npid=%d go=%s os=%s/%s %s\n",
		os.Getpid(), runtime.Version(), runtime.GOOS, runtime.GOARCH,
		time.Now().Format("2006-01-02 15:04:05.000"))
	installSignalHandler()
	return nil
}

// Enabled indica si el modo debug está activo.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

// Logf escribe una línea con marca de tiempo si el modo debug está activo.
// Es seguro llamarlo desde cualquier goroutine y cuando está desactivado.
func Logf(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if !enabled || file == nil {
		return
	}
	fmt.Fprintf(file, "[%s] %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

// DumpStacks vuelca todas las pilas de goroutines al log (para ver dónde está
// atascado un proceso congelado).
func DumpStacks(reason string) {
	mu.Lock()
	defer mu.Unlock()
	if !enabled || file == nil {
		return
	}
	buf := make([]byte, 64*1024)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, len(buf)*2) // se quedó corto: dobla el buffer
	}
	fmt.Fprintf(file, "\n===== GOROUTINE DUMP (%s) %s =====\n%s\n",
		reason, time.Now().Format("2006-01-02 15:04:05.000"), buf)
}
