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
	steps []model.Message
	seen  [][]model.Message
	i     int
}

func (p *scriptedProvider) Label() string { return "scripted" }

func (p *scriptedProvider) Chat(_ context.Context, messages []model.Message, _ []model.ToolDefinition, _ string, _, _ provider.DeltaFunc) (model.Message, error) {
	p.seen = append(p.seen, append([]model.Message(nil), messages...))
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
