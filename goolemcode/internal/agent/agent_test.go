package agent

import (
	"strings"
	"testing"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

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
