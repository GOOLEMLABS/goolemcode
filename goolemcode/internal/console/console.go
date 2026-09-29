// Package console serializa las escrituras a stdout. El eco de lo que el usuario
// teclea (desde el watchdog) y el streaming del agente corren en goroutines
// distintas; sin un candado común, sus líneas se entremezclarían y corromperían
// la pantalla. Todas las salidas interactivas pasan por aquí.
package console

import (
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
