package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

// fakeProvider devuelve respuestas/errores preprogramados por llamada.
type fakeProvider struct {
	label   string
	replies []model.Message
	errs    []error
	calls   int
}

func (f *fakeProvider) Label() string { return f.label }

func (f *fakeProvider) Chat(_ context.Context, _ []model.Message, _ []model.ToolDefinition, _ string, onDelta, _ DeltaFunc) (model.Message, error) {
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return model.Message{}, f.errs[i]
	}
	if i < len(f.replies) {
		return f.replies[i], nil
	}
	if onDelta != nil {
		onDelta("ok")
	}
	return model.Message{Role: model.RoleAssistant, Content: "ok"}, nil
}

// Regresión: el primario NO debe contar como fallo cuando responde bien. Antes
// se contaba cada llamada y el router preguntaba cada N turnos aunque todo fuera
// bien.
func TestSmartRouterDoesNotAskOnSuccess(t *testing.T) {
	primary := &fakeProvider{label: "primary"}
	secondary := &fakeProvider{label: "secondary"}
	r := NewSmartRouter(primary, secondary, false) // tareas simples → primario
	r.SetConsecutiveFallback(3)
	asked := 0
	r.SetAskUser(func(string) bool { asked++; return false })

	msgs := []model.Message{{Role: model.RoleUser, Content: "hola"}}
	for i := 0; i < 5; i++ {
		if _, err := r.Chat(context.Background(), msgs, nil, "", nil, nil); err != nil {
			t.Fatalf("Chat %d: %v", i, err)
		}
	}
	if asked != 0 {
		t.Fatalf("no debía preguntar con el primario respondiendo bien (preguntó %d veces)", asked)
	}
	if primary.calls != 5 {
		t.Fatalf("el primario debía usarse 5 veces, fue %d", primary.calls)
	}
}

// Tras 3 fallos CONSECUTIVOS reales del primario, pregunta una vez; si acepta,
// usa el secundario.
func TestSmartRouterAsksAfterConsecutiveFailures(t *testing.T) {
	primary := &fakeProvider{label: "primary", errs: []error{
		errors.New("boom"), errors.New("boom"), errors.New("boom"),
	}}
	secondary := &fakeProvider{label: "secondary"}
	r := NewSmartRouter(primary, secondary, false)
	r.SetConsecutiveFallback(3)
	asked := 0
	r.SetAskUser(func(string) bool { asked++; return true })

	msgs := []model.Message{{Role: model.RoleUser, Content: "hola"}}
	_, _ = r.Chat(context.Background(), msgs, nil, "", nil, nil)
	_, _ = r.Chat(context.Background(), msgs, nil, "", nil, nil)
	resp, err := r.Chat(context.Background(), msgs, nil, "", nil, nil) // 3er fallo → pregunta
	if err != nil {
		t.Fatalf("la 3ª debía resolverse con el secundario: %v", err)
	}
	if asked != 1 {
		t.Fatalf("debía preguntar exactamente 1 vez, preguntó %d", asked)
	}
	if secondary.calls != 1 {
		t.Fatalf("debía usar el secundario 1 vez, fue %d", secondary.calls)
	}
	if resp.Content != "ok" {
		t.Fatalf("respuesta inesperada: %+v", resp)
	}
}

// Un éxito intermedio resetea la racha de fallos del primario.
func TestSmartRouterSuccessResetsFailureStreak(t *testing.T) {
	primary := &fakeProvider{label: "primary", errs: []error{
		errors.New("boom"), nil, errors.New("boom"), errors.New("boom"),
	}}
	secondary := &fakeProvider{label: "secondary"}
	r := NewSmartRouter(primary, secondary, false)
	r.SetConsecutiveFallback(3)
	asked := 0
	r.SetAskUser(func(string) bool { asked++; return true })

	msgs := []model.Message{{Role: model.RoleUser, Content: "hola"}}
	for i := 0; i < 4; i++ {
		_, _ = r.Chat(context.Background(), msgs, nil, "", nil, nil)
	}
	if asked != 0 {
		t.Fatalf("un éxito intermedio debe resetear la racha (preguntó %d veces)", asked)
	}
}

// El turno del asistente con tool_calls debe serializar content:null (no ""),
// que es lo que exige la API OpenAI de DeepSeek.
func TestDeepSeekAssistantToolCallContentNull(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "lee x"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
			{ID: "c1", Name: "read_file", Arguments: map[string]any{"path": "x"}},
		}},
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{CallID: "c1", Content: "hola"}}},
	}
	built := dsBuildMessages("sys", msgs)

	b, err := json.Marshal(built)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// El mensaje del asistente debe tener content:null y tool_call_id en el tool.
	if !strings.Contains(s, `"tool_calls"`) || !strings.Contains(s, `"content":null`) {
		t.Fatalf("el turno con tool_calls debe llevar content:null: %s", s)
	}
	if !strings.Contains(s, `"tool_call_id":"c1"`) {
		t.Fatalf("el resultado de herramienta debe referenciar tool_call_id: %s", s)
	}
	// El system y el user sí llevan content string.
	if built[0].Role != "system" || built[0].Content == nil || *built[0].Content != "sys" {
		t.Fatalf("primer mensaje debe ser system con content 'sys'")
	}
}

// Los argumentos de un tool_call llegan troceados por varios eventos SSE y deben
// concatenarse por índice.
func TestDeepSeekStreamAssemblesChunkedToolArgs(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Voy a "}}]}`,
		`data: {"choices":[{"delta":{"content":"leer."}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"y.go\"}"}}]}}]}`,
		`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
		`data: {"usage":{"prompt_tokens":11,"completion_tokens":7}}`,
		`data: [DONE]`,
	}, "\n")

	var streamed strings.Builder
	msg, err := parseDeepSeekStream(strings.NewReader(stream), func(s string) { streamed.WriteString(s) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Voy a leer." {
		t.Fatalf("content mal ensamblado: %q", msg.Content)
	}
	if streamed.String() != "Voy a leer." {
		t.Fatalf("onDelta no recibió el texto: %q", streamed.String())
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("esperado 1 tool_call, hay %d", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call_x" || tc.Name != "read_file" {
		t.Fatalf("id/name mal: %+v", tc)
	}
	if got, _ := tc.Arguments["path"].(string); got != "y.go" {
		t.Fatalf("argumentos troceados mal concatenados: %+v", tc.Arguments)
	}
	if msg.StopReason != "tool_use" {
		t.Fatalf("stop_reason esperado tool_use, es %q", msg.StopReason)
	}
	if msg.Usage == nil || msg.Usage.InputTokens != 11 || msg.Usage.OutputTokens != 7 {
		t.Fatalf("uso mal capturado: %+v", msg.Usage)
	}
}

// El stream JSON-por-líneas de Ollama: texto por deltas + tool_call + uso final.
func TestOllamaStreamParsesTextToolAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		`{"message":{"role":"assistant","content":"Hola"},"done":false}`,
		`{"message":{"role":"assistant","content":" mundo"},"done":false}`,
		`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"list_dir","arguments":{"path":"."}}}]},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":42,"eval_count":9}`,
	}, "\n")

	var streamed strings.Builder
	msg, err := parseOllamaStream(strings.NewReader(stream), func(s string) { streamed.WriteString(s) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Hola mundo" || streamed.String() != "Hola mundo" {
		t.Fatalf("texto mal: content=%q streamed=%q", msg.Content, streamed.String())
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "list_dir" {
		t.Fatalf("tool_call mal: %+v", msg.ToolCalls)
	}
	if p, _ := msg.ToolCalls[0].Arguments["path"].(string); p != "." {
		t.Fatalf("argumentos mal: %+v", msg.ToolCalls[0].Arguments)
	}
	if msg.Usage == nil || msg.Usage.InputTokens != 42 || msg.Usage.OutputTokens != 9 {
		t.Fatalf("uso mal: %+v", msg.Usage)
	}
	if msg.StopReason != "tool_use" {
		t.Fatalf("stop_reason esperado tool_use, es %q", msg.StopReason)
	}
}

// qwen3 emite razonamiento en el campo "thinking": debe ir a onThinking, no a
// onDelta ni al contenido final.
func TestOllamaStreamEmitsThinking(t *testing.T) {
	stream := strings.Join([]string{
		`{"message":{"role":"assistant","thinking":"déjame "},"done":false}`,
		`{"message":{"role":"assistant","thinking":"pensar"},"done":false}`,
		`{"message":{"role":"assistant","content":"respuesta"},"done":true,"eval_count":3}`,
	}, "\n")
	var think, answer strings.Builder
	msg, err := parseOllamaStream(strings.NewReader(stream),
		func(s string) { answer.WriteString(s) },
		func(s string) { think.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if think.String() != "déjame pensar" {
		t.Fatalf("onThinking mal: %q", think.String())
	}
	if answer.String() != "respuesta" || msg.Content != "respuesta" {
		t.Fatalf("el thinking no debe filtrarse a la respuesta: answer=%q content=%q", answer.String(), msg.Content)
	}
}

// Un mensaje de usuario inyectado a mitad de turno (tras los tool_result, que
// también son rol "user" en Anthropic) debe fusionarse en el mismo mensaje, no
// emitirse como dos "user" consecutivos (la API exige roles alternos).
func TestAnthropicMergesConsecutiveUserMessages(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "inicial"},
		{Role: model.RoleAssistant, ProviderRaw: json.RawMessage(`[{"type":"tool_use","id":"t1","name":"read_file","input":{}}]`)},
		{Role: model.RoleTool, ToolResults: []model.ToolResult{{CallID: "t1", Content: "ok"}}},
		{Role: model.RoleUser, Content: "interrupción del usuario"},
	}
	out := toAPIMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("esperado 3 mensajes (tool_result + texto fusionados), hay %d: %+v", len(out), out)
	}
	if out[2]["role"] != "user" {
		t.Fatalf("el 3º mensaje debe ser user, es %v", out[2]["role"])
	}
	blocks, ok := out[2]["content"].([]map[string]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("el mensaje fusionado debe tener 2 bloques (tool_result + text): %+v", out[2]["content"])
	}
	if blocks[0]["type"] != "tool_result" || blocks[1]["type"] != "text" || blocks[1]["text"] != "interrupción del usuario" {
		t.Fatalf("fusión incorrecta: %+v", blocks)
	}
}

// Sin tool_calls, el stop_reason es end_turn.
func TestOllamaStreamPlainText(t *testing.T) {
	stream := `{"message":{"content":"solo texto"},"done":true,"prompt_eval_count":5,"eval_count":2}`
	msg, err := parseOllamaStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "solo texto" || msg.StopReason != "end_turn" {
		t.Fatalf("mal: %+v", msg)
	}
}
