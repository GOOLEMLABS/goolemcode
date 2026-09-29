// Package proc lanza comandos externos de forma NO interactiva y desacoplada de
// la terminal. Evita que herramientas como vi, less, `git rebase --continue` o
// prompts de contraseña se queden esperando el teclado (que el agente ocupa en
// modo raw durante el turno) y bloqueen el REPL.
//
// Claves:
//   - Setsid: el hijo arranca en una sesión nueva SIN terminal de control, así
//     que abrir /dev/tty falla y los programas interactivos fallan rápido en vez
//     de colgarse.
//   - Entorno no interactivo: GIT_EDITOR/GIT_SEQUENCE_EDITOR=true, PAGER=cat,
//     GIT_TERMINAL_PROMPT=0, EDITOR/VISUAL=true.
//   - Al cancelar/expirar se mata a TODO el grupo de procesos (no solo al hijo
//     directo), que era el origen de procesos git/vi huérfanos.
package proc

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Command prepara un *exec.Cmd con entorno no interactivo, sin terminal de
// control y con cancelación que mata al grupo entero. El llamante puede fijar
// Dir, combinar salidas, etc. como con exec.CommandContext.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = nonInteractiveEnv()
	cmd.Stdin = nil // os/exec: el hijo lee de /dev/null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Setsid hace que el PID sea líder de grupo: -pid mata a todo el grupo
		// (sh, git, vi, …), evitando huérfanos que sigan usando la terminal.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// Output ejecuta el comando y devuelve su stdout.
func Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return Command(ctx, name, args...).Output()
}

// CombinedOutput ejecuta el comando y devuelve stdout+stderr combinados.
func CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return Command(ctx, name, args...).CombinedOutput()
}

// Env devuelve el entorno no interactivo (os.Environ + overrides). Útil para
// quien necesite añadir sus propias variables (p. ej. los hooks).
func Env() []string { return nonInteractiveEnv() }

// nonInteractiveEnv parte del entorno real y fuerza las variables que evitan
// editores, pagers y prompts interactivos.
func nonInteractiveEnv() []string {
	m := map[string]string{}
	var order []string
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i >= 0 {
			k := e[:i]
			if _, ok := m[k]; !ok {
				order = append(order, k)
			}
			m[k] = e[i+1:]
		}
	}
	set := func(k, v string) {
		if _, ok := m[k]; !ok {
			order = append(order, k)
		}
		m[k] = v
	}
	set("GIT_EDITOR", "true")
	set("GIT_SEQUENCE_EDITOR", "true")
	set("GIT_TERMINAL_PROMPT", "0")
	set("GIT_PAGER", "cat")
	set("PAGER", "cat")
	set("EDITOR", "true")
	set("VISUAL", "true")
	set("LESS", "-FRX")

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+m[k])
	}
	return out
}
