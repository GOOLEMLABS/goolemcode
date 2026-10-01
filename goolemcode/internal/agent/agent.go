// Package agent implementa el bucle de control, independiente del proveedor.
// Ciclo: [input] -> [chat: texto/tool_calls] -> [checkpoint+permiso+ejecución]
// -> [inyección de resultados] -> [re-evaluación] -> [respuesta final | paso].
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GOOLEMLABS/goolemcode/internal/checkpoint"
	"github.com/GOOLEMLABS/goolemcode/internal/debug"
	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/provider"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
)

const systemPrompt = `You are GoolemCode, an AI coding assistant working on the user's project directory through tools.

- Explore before editing: use view_directory_tree and read_file (with line ranges for large files).
- To locate code or text, use grep (regex) instead of blindly reading files.
- To inspect the repository use git_status/git_diff/git_log; to commit changes use git_commit (will ask for confirmation). Do NOT commit unless the user asks.
- To create documentation or any file use write_file; for targeted changes, edit_file.
- YOU HAVE INTERNET ACCESS. For real-time or current information (weather, news, prices, events, versions) use web_search or web_fetch: NEVER say you cannot know the present or near future. Use today's date provided in the context.
- For weather use web_fetch on https://wttr.in/CITY?lang=en&T (plain text with multi-day forecast).
- AUTO-FIX: if execute_command returns a non-zero exit code, analyze the output, fix it, and retry until it passes.
- Commands run NON-INTERACTIVELY (no terminal): never run things that wait for input (editors like vi, password prompts, git rebase --continue without GIT_EDITOR). Use non-interactive flags/env: git commit -m, --no-edit, --yes, GIT_EDITOR=true, etc.
- You maintain a knowledge base in .md notes: check it with knowledge_list/knowledge_read/knowledge_search before starting, and save useful findings with knowledge_write (one note per topic, with a 1-line summary at top). Do not duplicate notes; update existing ones.
- Memory is per-project by default. Use scope:"global" in knowledge tools ONLY when the user explicitly requests global or cross-project shared memory.
- If you are genuinely stuck and need a decision or data you cannot deduce, use ask_user to ask. Do not abuse it: do not ask what you can figure out yourself with the tools.
- For multi-step work, plan with task_write (states pending/in_progress/done): mark in_progress the task you start and done when finished. If there are IN-PROGRESS TASKS at start, resume them. Check status with task_list.
- Take small, verifiable steps. When done, give a brief summary of what you did.
- SCRIPT LIBRARY: if you repeat the same operation (list directories, system summary, find files…), use script_run with the appropriate script instead of generating code from scratch. It saves tokens and execution time. If the script you need doesn't exist, create one with script_create (add a docstring with description and Args:). Scripts live in ~/.goolem/scripts/ and run with python3.`

// PermissionFunc decide si se autoriza una acción mutadora (true = adelante).
// Recibe el nombre de la herramienta (para recordar "siempre" por herramienta) y
// sus argumentos (para poder previsualizar, p. ej. el diff de una edición).
type PermissionFunc func(toolName string, args map[string]any) bool

// Hooks permite a la UI observar el progreso del turno.
type Hooks struct {
	OnDelta      func(string)
	OnThinking   func(string) // razonamiento del modelo (qwen3, deepseek-reasoner, Claude)
	OnToolCall   func(model.ToolCall)
	OnToolResult func(model.ToolResult)
	OnUserInput  func(string) // mensaje que el usuario escribió mientras el agente trabajaba
}

type Agent struct {
	prov      provider.Provider
	reg       *tools.Registry
	cp        *checkpoint.Manager
	permit    PermissionFunc
	system    string
	contextFn func() string // contexto dinámico (p. ej. índice de conocimiento) añadido cada turno
	maxSteps  int
	messages  []model.Message

	turnUsage     model.Usage // uso del último turno del usuario (varios pasos)
	sessionUsage  model.Usage // uso acumulado de la sesión
	contextBudget int         // presupuesto de tokens del historial (0 = ilimitado)
	// pending devuelve las líneas que el usuario escribió mientras el agente
	// trabajaba; se inyectan como mensajes de usuario entre pasos.
	pending func() []string
}

// TurnUsage devuelve el consumo de tokens del último turno.
func (a *Agent) TurnUsage() model.Usage { return a.turnUsage }

// SessionUsage devuelve el consumo acumulado de la sesión.
func (a *Agent) SessionUsage() model.Usage { return a.sessionUsage }

// defaultContextBudget es el presupuesto de tokens (estimado) del historial que
// se envía. Conservador para modelos con ventana de 32k; ajustable con
// SetContextBudget. La compactación descarta rondas viejas completas al superarlo.
const defaultContextBudget = 24000

func New(p provider.Provider, reg *tools.Registry, cp *checkpoint.Manager, permit PermissionFunc) *Agent {
	return &Agent{prov: p, reg: reg, cp: cp, permit: permit, system: systemPrompt, maxSteps: 200, contextBudget: defaultContextBudget}
}

// SetMaxSteps ajusta el máximo de pasos (rondas de chat/tool) por turno. Un tope
// bajo corta tareas largas a medias ("[Se alcanzó el máximo de pasos]") y obliga
// al usuario a pedir que continúe.
func (a *Agent) SetMaxSteps(n int) {
	if n > 0 {
		a.maxSteps = n
	}
}

// SetContextBudget ajusta el presupuesto de tokens del historial (0 = sin límite).
func (a *Agent) SetContextBudget(tokens int) { a.contextBudget = tokens }

// SetContextProvider registra una función cuyo texto se añade al system prompt
// en cada turno (se recalcula, así refleja cambios hechos durante la sesión).
func (a *Agent) SetContextProvider(fn func() string) { a.contextFn = fn }

// SetPendingInput registra la función que devuelve los mensajes que el usuario
// escribió mientras el agente trabajaba (y los consume). Se inyectan como
// mensajes de usuario al inicio de cada paso, de modo que el modelo puede
// atenderlos sin esperar a que termine el turno.
func (a *Agent) SetPendingInput(fn func() []string) { a.pending = fn }

// SetProvider cambia el proveedor LLM activo (para /model en caliente). El
// historial se conserva.
func (a *Agent) SetProvider(p provider.Provider) { a.prov = p }

func (a *Agent) Provider() provider.Provider { return a.prov }

func (a *Agent) systemPromptNow() string {
	if a.contextFn != nil {
		if extra := a.contextFn(); extra != "" {
			return a.system + "\n\n" + extra
		}
	}
	return a.system
}

// Snapshot devuelve el historial actual (para persistir la sesión).
func (a *Agent) Snapshot() []model.Message { return a.messages }

// Restore reemplaza el historial (para reanudar una sesión guardada).
func (a *Agent) Restore(msgs []model.Message) { a.messages = msgs }

func (a *Agent) Clear() {
	a.messages = nil
	if a.cp != nil {
		a.cp.Clear()
	}
}

// Run procesa un turno del usuario hasta la respuesta final o el tope de pasos.
func (a *Agent) Run(ctx context.Context, input string, h Hooks) (string, error) {
	return a.RunWithImages(ctx, input, nil, h)
}

// RunWithImages es como Run pero adjunta imágenes al mensaje del usuario (entrada
// multimodal; requiere un modelo con visión).
func (a *Agent) RunWithImages(ctx context.Context, input string, images []model.ImageData, h Hooks) (string, error) {
	a.messages = append(a.messages, model.Message{Role: model.RoleUser, Content: input, Images: images})
	a.turnUsage = model.Usage{}
	last := ""
	for step := 0; step < a.maxSteps; step++ {
		// Inyecta los mensajes que el usuario escribió mientras el agente
		// trabajaba, como turnos de usuario, antes del siguiente paso.
		if a.pending != nil {
			for _, msg := range a.pending() {
				debug.Logf("agent step %d: injecting queued user message (%d chars)", step, len(msg))
				a.messages = append(a.messages, model.Message{Role: model.RoleUser, Content: msg})
				if h.OnUserInput != nil {
					h.OnUserInput(msg)
				}
			}
		}
		if trimmed, dropped := compactHistory(a.messages, a.contextBudget); dropped > 0 {
			a.messages = trimmed
			if h.OnDelta != nil {
				h.OnDelta(fmt.Sprintf("\n[contexto: descartadas %d rondas antiguas para no exceder la ventana]\n", dropped))
			}
		}
		debug.Logf("agent step %d: chat (%d msgs)", step, len(a.messages))
		assistant, err := a.prov.Chat(ctx, a.messages, a.reg.Definitions(), a.systemPromptNow(), h.OnDelta, h.OnThinking)
		if err != nil {
			debug.Logf("agent step %d: chat error: %v", step, err)
			return "", err
		}
		if u := assistant.Usage; u != nil {
			a.turnUsage.InputTokens += u.InputTokens
			a.turnUsage.OutputTokens += u.OutputTokens
			a.sessionUsage.InputTokens += u.InputTokens
			a.sessionUsage.OutputTokens += u.OutputTokens
		}
		a.messages = append(a.messages, assistant)
		last = assistant.Content

		if assistant.StopReason == "refusal" && last == "" && len(assistant.ToolCalls) == 0 {
			return "El modelo rechazó la petición por motivos de seguridad.", nil
		}
		if len(assistant.ToolCalls) == 0 {
			debug.Logf("agent step %d: final answer", step)
			return last, nil
		}
		debug.Logf("agent step %d: %d tool call(s)", step, len(assistant.ToolCalls))

		results := make([]model.ToolResult, 0, len(assistant.ToolCalls))

		// Single checkpoint before ANY mutating tool executes
		hasMutating := false
		for _, call := range assistant.ToolCalls {
			if a.reg.IsMutating(call.Name) {
				hasMutating = true
				break
			}
		}
		if hasMutating && a.cp != nil {
			a.cp.Capture("multi-tool turn", "")
		}

		for _, call := range assistant.ToolCalls {
			if h.OnToolCall != nil {
				h.OnToolCall(call)
			}
			res := a.runTool(ctx, call)
			if h.OnToolResult != nil {
				h.OnToolResult(res)
			}
			results = append(results, res)
		}
		a.messages = append(a.messages, model.Message{Role: model.RoleTool, ToolResults: results})
	}
	return last + "\n\n[Se alcanzó el máximo de pasos]", nil
}

func (a *Agent) runTool(ctx context.Context, call model.ToolCall) model.ToolResult {
	target := ""
	if p, ok := call.Arguments["path"].(string); ok {
		target = p
	}
	if a.cp != nil && target != "" {
		a.cp.Track(target)
	}
	if a.reg.IsMutating(call.Name) {
		if a.permit != nil {
			if !a.permit(call.Name, call.Arguments) {
				return model.ToolResult{CallID: call.ID, Content: "Usuario denegó la ejecución.", IsError: true}
			}
		}
	}
	return a.reg.Execute(ctx, call)
}

// estimateTokens aproxima los tokens de un mensaje (~4 chars/token) contando
// texto, argumentos de tool_calls, resultados de herramientas y los bloques
// crudos del proveedor (thinking/tool_use de Claude).
func estimateTokens(m model.Message) int {
	n := len(m.Content) + len(m.ProviderRaw)
	for _, tc := range m.ToolCalls {
		n += len(tc.Name)
		if b, err := json.Marshal(tc.Arguments); err == nil {
			n += len(b)
		}
	}
	for _, tr := range m.ToolResults {
		n += len(tr.Content)
	}
	return n/4 + 4
}

// compactHistory descarta las rondas MÁS ANTIGUAS completas (cada ronda empieza
// en un mensaje RoleUser real; los resultados de herramientas son RoleTool) hasta
// que la estimación cabe en budget. Nunca descarta la ronda en curso (la última)
// ni parte una ronda, así que la correspondencia tool_use↔tool_result y los
// bloques de thinking de Claude quedan intactos. budget<=0 desactiva la poda.
// Devuelve el historial (posiblemente recortado) y cuántas rondas se descartaron.
func compactHistory(msgs []model.Message, budget int) ([]model.Message, int) {
	if budget <= 0 || len(msgs) == 0 {
		return msgs, 0
	}
	total := 0
	for _, m := range msgs {
		total += estimateTokens(m)
	}
	if total <= budget {
		return msgs, 0
	}
	// Índices donde empieza cada ronda (mensajes del usuario reales).
	var starts []int
	for i, m := range msgs {
		if m.Role == model.RoleUser {
			starts = append(starts, i)
		}
	}
	dropped := 0
	// Mientras haya más de una ronda y sigamos por encima del presupuesto,
	// elimina la ronda más antigua entera.
	for len(starts) > 1 && total > budget {
		end := starts[1] // fin exclusivo de la ronda más antigua
		for i := 0; i < end; i++ {
			total -= estimateTokens(msgs[i])
		}
		msgs = msgs[end:]
		starts = starts[1:]
		for i := range starts {
			starts[i] -= end
		}
		dropped++
	}
	return msgs, dropped
}
