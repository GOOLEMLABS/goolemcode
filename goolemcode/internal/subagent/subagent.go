// Package subagent registra la herramienta spawn_agent: lanza un agente HIJO con
// su propio contexto y su propio bucle de herramientas para resolver una subtarea
// acotada, y devuelve solo su resultado final. Así el trabajo independiente
// (explorar un subsistema, analizar muchos ficheros, implementar algo concreto)
// no ensucia el contexto de la conversación principal.
//
// Soporta lanzamiento PARALELO: múltiples llamadas a spawn_agent concurrentes
// ejecutan sub-agentes en paralelo, cada uno con su propio contador de anidamiento.
package subagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/GOOLEMLABS/goolemcode/internal/agent"
	"github.com/GOOLEMLABS/goolemcode/internal/checkpoint"
	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/provider"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
	"github.com/GOOLEMLABS/goolemcode/internal/usage"
)

// maxDepth limita el anidamiento: el agente principal (nivel 0) puede lanzar un
// sub-agente (nivel 1), pero este ya no puede lanzar más. Evita recursión
// infinita al compartir el mismo registro de herramientas.
const maxDepth = 1

// Register añade spawn_agent con soporte PARALELO: varias llamadas concurrentes
// ejecutan sub-agentes en paralelo (cada uno con su propio contador de anidamiento
// aislado vía goroutine-local). No hay mutex global que serialice.
func Register(reg *tools.Registry, prov provider.Provider, cp *checkpoint.Manager, permit agent.PermissionFunc, tracker *usage.Tracker, baseContext func() string) {
	reg.Register(model.ToolDefinition{
		Name: "spawn_agent",
		Description: "Launches a SUB-AGENT with its own context to solve a bounded independent subtask " +
			"(e.g. explore a subsystem, analyze several files, implement a specific piece) and returns its " +
			"result. Use it for work that would clutter your context; describe the task autonomously, because the " +
			"sub-agent does NOT see this conversation. Do not use it for trivial steps you can do directly.\n\n" +
			"You can launch MULTIPLE sub-agents in parallel by sending multiple spawn_agent calls concurrently. " +
			"Each sub-agent runs in its own goroutine.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{"type": "string", "description": "Self-contained instruction for the sub-agent"},
			},
			"required": []string{"task"},
		},
		Mutating: false, // el sub-agente pasa sus propias acciones por el gate de permisos
	}, func(ctx context.Context, args map[string]any) (string, error) {
		return runSubAgent(ctx, args, prov, reg, cp, permit, tracker, baseContext, 0)
	})
}

// runSubAgent ejecuta un sub-agente en la goroutine actual. depth es el nivel de
// anidamiento actual; esta función lo incrementa para sus hijos.
func runSubAgent(ctx context.Context, args map[string]any, prov provider.Provider, reg *tools.Registry, cp *checkpoint.Manager, permit agent.PermissionFunc, tracker *usage.Tracker, baseContext func() string, depth int) (string, error) {
	task, _ := args["task"].(string)
	if strings.TrimSpace(task) == "" {
		return "", fmt.Errorf("task is empty")
	}

	if depth >= maxDepth {
		return "A sub-agent cannot launch more sub-agents. Solve the subtask directly.", nil
	}

	child := agent.New(prov, reg, cp, permit)
	if baseContext != nil {
		child.SetContextProvider(baseContext)
	}

	fmt.Printf("\n  ⟳ sub-agent [level %d]: %s\n", depth+1, truncate(task, 100))
	h := agent.Hooks{
		OnToolCall: func(c model.ToolCall) {
			fmt.Printf("    → %s\n", c.Name)
		},
		OnToolResult: func(r model.ToolResult) {
			tag := "✓"
			if r.IsError {
				tag = "✗"
			}
			fmt.Printf("      %s %s\n", tag, truncate(strings.ReplaceAll(r.Content, "\n", " "), 100))
		},
	}

	result, err := child.Run(ctx, task, h)
	if err != nil {
		return "", err
	}
	if u := child.TurnUsage(); u.InputTokens > 0 || u.OutputTokens > 0 {
		tracker.Add(prov.Label()+" (sub-agente)", u)
	}
	fmt.Printf("  ⟲ sub-agent [level %d] finished\n", depth+1)
	return result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
