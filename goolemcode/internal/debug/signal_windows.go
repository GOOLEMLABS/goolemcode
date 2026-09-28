//go:build windows

package debug

// installSignalHandler es un no-op en Windows (no hay SIGUSR1).
func installSignalHandler() {}
