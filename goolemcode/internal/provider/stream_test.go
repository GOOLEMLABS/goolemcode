package provider

import (
	"io"
	"strings"
	"testing"
	"time"
)

// Un stream sin datos debe abortarse pasado el timeout (no colgarse).
func TestStallGuardAbortsIdleStream(t *testing.T) {
	pr, pw := io.Pipe()
	guard := newStallGuard(pr, pw.Close, 50*time.Millisecond)
	defer guard.stop()

	start := time.Now()
	_, err := io.ReadAll(guard)
	if err == nil {
		t.Fatal("esperaba error de stream estancado")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("error inesperado: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("tardó demasiado en abortar: %v", d)
	}
}

// Un stream con datos que fluyen por debajo del timeout no debe abortarse.
func TestStallGuardResetsOnData(t *testing.T) {
	pr, pw := io.Pipe()
	guard := newStallGuard(pr, pw.Close, 100*time.Millisecond)
	defer guard.stop()

	go func() {
		for i := 0; i < 5; i++ {
			_, _ = pw.Write([]byte("x"))
			time.Sleep(30 * time.Millisecond) // < timeout: reinicia la cuenta
		}
		_ = pw.Close()
	}()

	data, err := io.ReadAll(guard)
	if err != nil {
		t.Fatalf("no debía abortar con datos fluyendo: %v", err)
	}
	if string(data) != "xxxxx" {
		t.Fatalf("datos=%q", data)
	}
}

// Los datos que sí llegan se pasan intactos.
func TestStallGuardPassesData(t *testing.T) {
	pr, pw := io.Pipe()
	guard := newStallGuard(pr, pw.Close, time.Second)
	defer guard.stop()

	go func() {
		_, _ = pw.Write([]byte("hola"))
		_ = pw.Close()
	}()

	data, err := io.ReadAll(guard)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "hola" {
		t.Fatalf("datos=%q", data)
	}
}
