package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

func assistantRaw(t *testing.T, blocks []map[string]any) model.Message {
	t.Helper()
	b, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return model.Message{
		Role:        model.RoleAssistant,
		Content:     "ok",
		ToolCalls:   []model.ToolCall{{ID: "t1", Name: "write_file", Arguments: map[string]any{"path": "x"}}},
		ProviderRaw: b,
	}
}

// En turnos cerrados se elimina el thinking y se recortan los inputs enormes,
// pero se conserva el bloque tool_use (necesario para emparejar tool_result).
func TestAnthropicTrimHistoryDropsOldThinkingAndShrinksArgs(t *testing.T) {
	big := strings.Repeat("x", 5000)
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "hazlo"},
		assistantRaw(t, []map[string]any{
			{"type": "thinking", "thinking": "secreto", "signature": "sig"},
			{"type": "text", "text": "voy"},
			{"type": "tool_use", "id": "t1", "name": "write_file", "input": map[string]any{"path": "x", "content": big}},
		}),
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{CallID: "t1", Content: "hecho"}}},
		{Role: model.RoleUser, Content: "siguiente"}, // el assistant ya es histórico
	}

	out := (&Anthropic{}).TrimHistory(msgs, 3) // el turno nuevo empieza en "siguiente"
	raw := string(out[1].ProviderRaw)
	if strings.Contains(raw, "thinking") || strings.Contains(raw, "secreto") {
		t.Fatalf("no debía conservar thinking histórico: %s", raw)
	}
	if !strings.Contains(raw, "tool_use") || !strings.Contains(raw, "t1") {
		t.Fatalf("debía conservar el tool_use: %s", raw)
	}
	if strings.Contains(raw, big) {
		t.Fatal("debía recortar el input enorme del tool_use")
	}
	if !strings.Contains(raw, "bytes omitted") {
		t.Fatalf("debía marcar el input recortado: %s", raw)
	}
}

// Mientras se responden tool_results, el último assistant conserva su thinking
// (la API de Anthropic lo exige).
func TestAnthropicTrimHistoryPreservesActiveThinking(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "hazlo"},
		assistantRaw(t, []map[string]any{
			{"type": "thinking", "thinking": "necesario", "signature": "sig"},
			{"type": "tool_use", "id": "t1", "name": "write_file", "input": map[string]any{"path": "x"}},
		}),
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{CallID: "t1", Content: "hecho"}}},
	}
	out := (&Anthropic{}).TrimHistory(msgs, 0) // todo el historial pertenece al turno activo
	raw := string(out[1].ProviderRaw)
	if !strings.Contains(raw, "necesario") || !strings.Contains(raw, "signature") {
		t.Fatalf("el thinking activo debía conservarse: %s", raw)
	}
}

// Un assistant sin tool_use solo necesita su Content: se tira ProviderRaw
// (thinking incluido) para no reenviarlo nunca.
func TestAnthropicTrimHistoryDropsRawWithoutToolUse(t *testing.T) {
	b, _ := json.Marshal([]map[string]any{
		{"type": "thinking", "thinking": "razonamiento", "signature": "sig"},
		{"type": "text", "text": "respuesta"},
	})
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "hola"},
		{Role: model.RoleAssistant, Content: "respuesta", ProviderRaw: b},
		{Role: model.RoleUser, Content: "otra"},
	}
	out := (&Anthropic{}).TrimHistory(msgs, 2) // el assistant ya es un turno cerrado
	if len(out[1].ProviderRaw) != 0 {
		t.Fatalf("ProviderRaw debía quedar vacío: %s", out[1].ProviderRaw)
	}
}

func TestMarkLastCacheableBlockSkipsThinking(t *testing.T) {
	msg := map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "thinking", "thinking": "x"},
		map[string]any{"type": "tool_use", "id": "t1", "name": "n", "input": map[string]any{}},
	}}
	if !markLastCacheableBlock(msg) {
		t.Fatal("debía encontrar un bloque cacheable")
	}
	blocks := msg["content"].([]map[string]any)
	if _, ok := blocks[0]["cache_control"]; ok {
		t.Fatal("no se debe poner cache_control en thinking")
	}
	if cc, ok := blocks[1]["cache_control"].(map[string]any); !ok || cc["type"] != "ephemeral" {
		t.Fatalf("el tool_use debía llevar cache_control: %v", blocks[1])
	}
}
