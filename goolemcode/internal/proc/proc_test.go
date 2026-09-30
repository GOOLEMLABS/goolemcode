package proc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/console"
)

// El patrón que usan las herramientas (Stdout=Stderr=Capture) captura ambos
// flujos y los reenvía en vivo.
func TestCommandCapturesStdoutAndStderr(t *testing.T) {
	cap := console.NewCapture(1000)
	cmd := Command(context.Background(), "sh", "-c", "echo out; echo err >&2")
	cmd.Stdout = cap
	cmd.Stderr = cap
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	s := cap.String()
	if !strings.Contains(s, "out") || !strings.Contains(s, "err") {
		t.Fatalf("salida capturada=%q", s)
	}
}

func TestEnvIsNonInteractive(t *testing.T) {
	m := map[string]string{}
	for _, e := range Env() {
		if i := strings.IndexByte(e, '='); i >= 0 {
			m[e[:i]] = e[i+1:]
		}
	}
	want := map[string]string{
		"GIT_EDITOR":          "true",
		"GIT_SEQUENCE_EDITOR": "true",
		"GIT_TERMINAL_PROMPT": "0",
		"PAGER":               "cat",
		"EDITOR":              "true",
		"VISUAL":              "true",
	}
	for k, v := range want {
		if m[k] != v {
			t.Fatalf("%s=%q, esperado %q", k, m[k], v)
		}
	}
}

// Al cancelar, el comando debe volver de inmediato (no quedarse colgado).
func TestCancelReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = CombinedOutput(ctx, "sh", "-c", "sleep 30")
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("el comando no se canceló (seguiría colgado)")
	}
}

// Reproduce el caso real: `git commit` sin -m abriría un editor; con GIT_EDITOR=true
// debe fallar rápido en vez de quedarse esperando el teclado.
func TestGitCommitWithoutMessageDoesNotHang(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no disponible")
	}
	dir := t.TempDir()
	must := func(args ...string) {
		t.Helper()
		cmd := Command(context.Background(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	must("init", "-q")
	must("config", "user.email", "t@example.com")
	must("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	must("add", "-A")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := Command(ctx, "git", "commit")
	cmd.Dir = dir
	done := make(chan struct{})
	go func() { _ = cmd.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("git commit sin -m se colgó (abrió un editor interactivo)")
	}
}

// Al cancelar se mata a TODO el grupo de procesos, no solo al hijo directo: un
// proceso nieto en segundo plano no debe sobrevivir.
func TestCancelKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cmd := Command(ctx, "sh", "-c", "(sleep 2; touch "+marker+") & wait")
		cmd.Dir = dir
		_ = cmd.Run()
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	time.Sleep(2500 * time.Millisecond) // más que el sleep del nieto
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("el proceso nieto sobrevivió a la cancelación: el grupo no se mató")
	}
}
