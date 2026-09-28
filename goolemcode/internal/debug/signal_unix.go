//go:build !windows

package debug

import (
	"os"
	"os/signal"
	"syscall"
)

// installSignalHandler vuelca las pilas de goroutines al recibir SIGUSR1
// (`kill -USR1 <pid>`), sin matar el proceso.
func installSignalHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			DumpStacks("SIGUSR1")
		}
	}()
}
