package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/provider"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
)

// scriptedProvider devuelve respuestas preprogramadas y guarda el historial
// recibido en cada llamada a Chat.
type scriptedProvider struct {
	steps   []model.Message
	seen    [][]model.Message
	systems []string
	i       int
}

func (p *scriptedProvider) Label() string { return "scripted" }

func (p *scriptedProvider) Chat(_ context.Context, messages []model.Message, _ []model.ToolDefinition, system string, _, _ provider.DeltaFunc) (model.Message, error) {
	p.seen = append(p.seen, append([]model.Message(nil), messages...))
	p.systems = append(p.systems, system)
	msg := p.steps[p.i]
	p.i++
	return msg, nil
}

// Regresión: lo que el usuario escribe mientras el agente trabaja se inyecta
// como mensaje de usuario entre pasos (antes del siguiente Chat).
func TestAgentInjectsQueuedInputBetweenSteps(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(model.ToolDefinition{Name: "noop", Description: "x", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, map[string]any) (string, error) { return "ok", nil })

	prov := &scriptedProvider{steps: []model.Message{
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: "noop", Arguments: map[string]any{}}}},
		{Role: model.RoleAssistant, Content: "final"},
	}}
	ag := New(prov, reg, nil, nil)

	calls := 0
	ag.SetPendingInput(func() []string {
		calls++
		if calls == 2 { // solo en el 2º paso (entre pasos, no al inicio)
			return []string{"mensaje mientras trabajaba"}
		}
		return nil
	})

	var injected []string
	h := Hooks{OnUserInput: func(s string) { injected = append(injected, s) }}
	if _, err := ag.Run(context.Background(), "hola", h); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(injected) != 1 || injected[0] != "mensaje mientras trabajaba" {
		t.Fatalf("no se inyectó el mensaje encolado: %v", injected)
	}
	if len(prov.seen) != 2 {
		t.Fatalf("esperado 2 llamadas a Chat, hubo %d", len(prov.seen))
	}
	found := false
	for _, m := range prov.seen[1] {
		if m.Role == model.RoleUser && m.Content == "mensaje mientras trabajaba" {
			found = true
		}
	}
	if !found {
		t.Fatalf("el 2º Chat no recibió el mensaje inyectado: %+v", prov.seen[1])
	}
}

// buildRound crea una ronda: user → assistant(tool_call) → tool → assistant(texto).
func buildRound(userText, filler string) []model.Message {
	return []model.Message{
		{Role: model.RoleUser, Content: userText},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: "read_file", Arguments: map[string]any{"path": "x"}}}},
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{CallID: "c1", Content: filler}}},
		{Role: model.RoleAssistant, Content: "listo"},
	}
}

func TestCompactHistoryUnderBudgetNoop(t *testing.T) {
	msgs := buildRound("hola", "poco texto")
	out, dropped := compactHistory(msgs, 100000)
	if dropped != 0 || len(out) != len(msgs) {
		t.Fatalf("no debía compactar: dropped=%d len=%d", dropped, len(out))
	}
}

func TestCompactHistoryDropsOldestWholeRounds(t *testing.T) {
	big := strings.Repeat("x", 8000) // ~2000 tokens por ronda
	var msgs []model.Message
	msgs = append(msgs, buildRound("ronda1", big)...)
	msgs = append(msgs, buildRound("ronda2", big)...)
	msgs = append(msgs, buildRound("ronda3", big)...) // la actual

	out, dropped := compactHistory(msgs, 2500) // solo cabe ~1 ronda
	if dropped == 0 {
		t.Fatal("debía descartar al menos una ronda")
	}
	// La primera vieja debe haber desaparecido; la última (actual) debe seguir.
	if out[0].Role != model.RoleUser {
		t.Fatalf("el historial recortado debe empezar en un RoleUser, no %v", out[0].Role)
	}
	if got := out[0].Content; got == "ronda1" {
		t.Fatal("la ronda más antigua no se descartó")
	}
	if last := out[len(out)-1]; last.Role != model.RoleAssistant {
		t.Fatalf("la ronda actual debe quedar intacta, último rol=%v", last.Role)
	}
}

func TestCompactHistoryPreservesPairing(t *testing.T) {
	big := strings.Repeat("y", 8000)
	var msgs []model.Message
	for i := 0; i < 5; i++ {
		msgs = append(msgs, buildRound("r", big)...)
	}
	out, _ := compactHistory(msgs, 2500)

	// Todo RoleTool debe ir precedido por un assistant con tool_calls, y todo
	// tool_call debe conservar su tool_result: verificamos que no empieza en
	// RoleTool (huérfano) y que los CallID casan dentro del recorte.
	if out[0].Role == model.RoleTool {
		t.Fatal("el recorte dejó un tool_result huérfano al inicio")
	}
	openCalls := map[string]bool{}
	for _, m := range out {
		for _, tc := range m.ToolCalls {
			openCalls[tc.ID] = true
		}
		for _, tr := range m.ToolResults {
			if !openCalls[tr.CallID] {
				t.Fatalf("tool_result %s sin su tool_use en el recorte", tr.CallID)
			}
		}
	}
}

func TestCompactHistoryKeepsCurrentRound(t *testing.T) {
	// Aunque una sola ronda exceda el presupuesto, nunca se descarta.
	big := strings.Repeat("z", 40000)
	msgs := buildRound("única", big)
	out, dropped := compactHistory(msgs, 10)
	if dropped != 0 || len(out) != len(msgs) {
		t.Fatalf("no debe descartar la única ronda: dropped=%d len=%d", dropped, len(out))
	}
}

// El contexto dinámico va en un RoleSystem justo antes del RoleUser de su turno.
// Al descartar una ronda antigua no debemos llevarnos el contexto de la ronda
// que se conserva.
func TestCompactHistoryKeepsNextRoundContext(t *testing.T) {
	big := strings.Repeat("x", 8000)
	msgs := []model.Message{
		{Role: model.RoleSystem, Content: "CONTEXTO-1"},
		{Role: model.RoleUser, Content: "ronda1"},
		{Role: model.RoleAssistant, Content: strings.Repeat("a", 2000)},
		{Role: model.RoleSystem, Content: "CONTEXTO-2"},
		{Role: model.RoleUser, Content: "ronda2"},
		{Role: model.RoleAssistant, Content: big},
	}
	out, dropped := compactHistory(msgs, 2500)
	if dropped == 0 {
		t.Fatal("debía descartar la primera ronda")
	}
	if out[0].Role != model.RoleSystem || out[0].Content != "CONTEXTO-2" {
		t.Fatalf("debía conservar el contexto de la ronda actual, inicio=%+v", out[0])
	}
}

func TestPruneTurnSummarizesOldResultsAndShrinksArgs(t *testing.T) {
	big := strings.Repeat("r", 5000)
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "tarea"},
	}
	for i := 0; i < 5; i++ {
		msgs = append(msgs,
			model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{
				ID: "c" + string(rune('0'+i)), Name: "write_file",
				Arguments: map[string]any{"path": "p", "content": big},
			}}},
			model.Message{Role: model.RoleTool, ToolResults: []model.ToolResult{{
				CallID: "c" + string(rune('0'+i)), Content: big,
			}}},
		)
	}
	out, pruned := pruneTurn(msgs, 0, 2000)
	if pruned == 0 {
		t.Fatal("debía podar el turno")
	}
	// Los primeros resultados quedan resumidos; el último se conserva entero.
	first := out[1+1].ToolResults[0].Content // primer RoleTool
	if !strings.Contains(first, "output omitted") {
		t.Fatalf("el resultado antiguo debía resumirse: %q", first[:80])
	}
	lastIdx := len(out) - 1
	last := out[lastIdx].ToolResults[0].Content
	if last != big {
		t.Fatal("el último resultado debía conservarse íntegro")
	}
	// Los args antiguos se recortan, pero se conserva id/nombre/path.
	args := out[1].ToolCalls[0]
	if args.Name != "write_file" || args.ID != "c0" || args.Arguments["path"] != "p" {
		t.Fatalf("debía conservar identidad de la acción: %+v", args)
	}
	if args.Arguments["content"] == big {
		t.Fatal("debía recortar el content del write_file antiguo")
	}
	// Pairing intacto.
	for _, m := range out {
		for _, tr := range m.ToolResults {
			found := false
			for _, prev := range out {
				for _, tc := range prev.ToolCalls {
					if tc.ID == tr.CallID {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("tool_result %s sin tool_use", tr.CallID)
			}
		}
	}
}

// El contexto dinámico va UNA vez por turno como RoleSystem, delante del
// mensaje del usuario, y el system prompt que recibe el proveedor es estático.
func TestAgentContextIsPerTurnSystemMessage(t *testing.T) {
	reg := tools.NewRegistry()
	prov := &scriptedProvider{steps: []model.Message{{Role: model.RoleAssistant, Content: "ok"}}}
	ag := New(prov, reg, nil, nil)
	ag.SetContextProvider(func() string { return "CTX-DINAMICO" })

	if _, err := ag.Run(context.Background(), "hola", Hooks{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	seen := prov.seen[0]
	if len(seen) < 2 {
		t.Fatalf("historial inesperado: %+v", seen)
	}
	if seen[0].Role != model.RoleSystem || !strings.Contains(seen[0].Content, "CTX-DINAMICO") {
		t.Fatalf("falta el contexto como RoleSystem: %+v", seen[0])
	}
	if seen[1].Role != model.RoleUser || seen[1].Content != "hola" {
		t.Fatalf("el mensaje del usuario debía ir después: %+v", seen[1])
	}
	if strings.Contains(prov.systems[0], "CTX-DINAMICO") {
		t.Fatal("el contexto no debe ir en el system prompt (rompería el caché)")
	}
}

// Un mensaje encolado a mitad de turno (precedido por RoleTool) no abre una
// ronda nueva: no debe provocar que se descarte el inicio del turno en curso.
func TestCompactHistoryQueuedInputDoesNotStartRound(t *testing.T) {
	big := strings.Repeat("q", 8000)
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "tarea"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c1", Name: "read_file"}}},
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{CallID: "c1", Content: big}}},
		{Role: model.RoleUser, Content: "mensaje encolado"},
		{Role: model.RoleAssistant, Content: "sigo"},
	}
	out, dropped := compactHistory(msgs, 100)
	if dropped != 0 || len(out) != len(msgs) {
		t.Fatalf("el mensaje encolado no debía abrir ronda: dropped=%d len=%d", dropped, len(out))
	}
}

// El contexto dinámico (p. ej. un CLAUDE.md grande) no debe consumir el
// presupuesto intra-turno: si lo hiciera, cada resultado de tool se resumiría al
// instante y el modelo lo releería en bucle hasta agotar los pasos.
func TestAgentLargeContextDoesNotForceIntraTurnPruning(t *testing.T) {
	reg := tools.NewRegistry()
	blob := strings.Repeat("r", 4000) // ~1000 tokens
	reg.Register(model.ToolDefinition{Name: "blob", Description: "x", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, map[string]any) (string, error) { return blob, nil })

	var steps []model.Message
	for i := 0; i < 5; i++ { // 5 resultados ≈ 5000 tokens: caben en 16000
		steps = append(steps, model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{
			ID: "c" + string(rune('0'+i)), Name: "blob", Arguments: map[string]any{},
		}}})
	}
	steps = append(steps, model.Message{Role: model.RoleAssistant, Content: "final"})
	prov := &scriptedProvider{steps: steps}
	ag := New(prov, reg, nil, nil)
	// ~12 000 tokens de contexto: si contara, 12000+5000 > 16000 y el contexto
	// agotaría el presupuesto desde el principio.
	ag.SetContextProvider(func() string { return strings.Repeat("c", 48*1024) })

	if _, err := ag.Run(context.Background(), "tarea", Hooks{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	seen := prov.seen[5] // ya incluye los 5 resultados
	if len(seen) == 0 {
		t.Fatal("sin mensajes")
	}
	// El PRIMER resultado solo se resumiría si el contexto consumiera el
	// presupuesto (con keepRecentToolResults=3 se conservan los últimos 3).
	for _, m := range seen {
		for _, tr := range m.ToolResults {
			if tr.CallID == "c0" && tr.Content != blob {
				t.Fatalf("el primer resultado no debía resumirse por culpa del contexto: %q", tr.Content[:60])
			}
		}
	}
}
