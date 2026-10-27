package provider

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

// Anthropic habla con /v1/messages por HTTP plano (sin SDK → cero deps), con
// STREAMING SSE. Usa claude-opus-4-8, thinking adaptativo y output_config.effort,
// y tool-calling nativo. Reconstruye los content blocks (texto/thinking/tool_use)
// para devolverlos sin modificar en el siguiente turno: la API rechaza bloques
// de thinking alterados, y los tool_result deben referenciar el tool_use_id.
type Anthropic struct {
	APIKey    string
	Model     string
	MaxTokens int
	Effort    string
	client    *http.Client
}

func NewAnthropic(modelID string, maxTokens int, effort string) *Anthropic {
	return &Anthropic{
		APIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		Model:     modelID,
		MaxTokens: maxTokens,
		Effort:    effort,
		client:    &http.Client{Timeout: 10 * time.Minute},
	}
}

func (a *Anthropic) Label() string { return fmt.Sprintf("Claude (%s)", a.Model) }

type antCacheControl struct {
	Type string `json:"type"`
}

type antSystemBlock struct {
	Type         string           `json:"type"`
	Text         string           `json:"text"`
	CacheControl *antCacheControl `json:"cache_control,omitempty"`
}

type antTool struct {
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	InputSchema  map[string]any   `json:"input_schema"`
	CacheControl *antCacheControl `json:"cache_control,omitempty"`
}
type antReq struct {
	Model        string           `json:"model"`
	MaxTokens    int              `json:"max_tokens"`
	System       []antSystemBlock `json:"system,omitempty"`
	Messages     []map[string]any `json:"messages"`
	Tools        []antTool        `json:"tools,omitempty"`
	Thinking     map[string]any   `json:"thinking,omitempty"`
	OutputConfig map[string]any   `json:"output_config,omitempty"`
	Stream       bool             `json:"stream"`
}

func (a *Anthropic) Chat(ctx context.Context, messages []model.Message, tools []model.ToolDefinition, system string, onDelta, onThinking DeltaFunc) (model.Message, error) {
	req := antReq{
		Model:        a.Model,
		MaxTokens:    a.MaxTokens,
		Thinking:     map[string]any{"type": "adaptive"},
		OutputConfig: map[string]any{"effort": a.Effort},
		Stream:       true,
	}
	// El system prompt es estable (contexto dinámico va como mensaje aparte):
	// se marca como prefijo cacheable.
	if system != "" {
		req.System = []antSystemBlock{{
			Type: "text", Text: system,
			CacheControl: &antCacheControl{Type: "ephemeral"},
		}}
	}
	for i, t := range tools {
		at := antTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
		if i == len(tools)-1 { // último schema: fin del prefijo estático de tools
			at.CacheControl = &antCacheControl{Type: "ephemeral"}
		}
		req.Tools = append(req.Tools, at)
	}
	req.Messages = toAPIMessages(messages)
	addCacheBreakpoints(req.Messages)

	body, err := json.Marshal(req)
	if err != nil {
		return model.Message{}, err
	}
	headers := map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         a.APIKey,
		"anthropic-version": "2023-06-01",
		"Accept":            "text/event-stream",
	}
	resp, err := doWithRetry(ctx, a.client, http.MethodPost, "https://api.anthropic.com/v1/messages", headers, body, 3)
	if err != nil {
		return model.Message{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return model.Message{}, fmt.Errorf("anthropic HTTP %d: %s", resp.StatusCode, string(b))
	}
	guard := newStallGuard(resp.Body, resp.Body.Close, StreamIdleTimeout)
	defer guard.stop()
	return parseSSE(guard, onDelta, onThinking)
}

// toAPIMessages traduce el historial neutral al formato de Anthropic.
func toAPIMessages(messages []model.Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case model.RoleUser:
			if len(m.Images) > 0 { // contenido como array: texto + imágenes
				blocks := []map[string]any{}
				for _, img := range m.Images {
					blocks = append(blocks, map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": img.Media,
							"data":       base64.StdEncoding.EncodeToString(img.Bytes),
						},
					})
				}
				if m.Content != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
				}
				out = appendUser(out, blocks)
			} else {
				out = appendUser(out, m.Content)
			}
		case model.RoleSystem:
			// Anthropic no admite role "system" dentro de messages: el contexto
			// dinámico (fecha, notas, tareas) viaja como bloque de usuario, fuera
			// del system cacheable.
			if m.Content != "" {
				out = appendUser(out, "[AUTO-GENERATED CONTEXT]\n"+m.Content)
			}
		case model.RoleAssistant:
			if len(m.ProviderRaw) > 0 {
				var raw any
				_ = json.Unmarshal(m.ProviderRaw, &raw)
				out = append(out, map[string]any{"role": "assistant", "content": raw})
			} else {
				out = append(out, map[string]any{"role": "assistant", "content": m.Content})
			}
		case model.RoleTool:
			blocks := make([]map[string]any, 0, len(m.ToolResults))
			for _, r := range m.ToolResults {
				blocks = append(blocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": r.CallID,
					"content":     r.Content,
					"is_error":    r.IsError,
				})
			}
			out = appendUser(out, blocks)
		}
	}
	return out
}

// appendUser añade un mensaje de usuario, fusionándolo con el anterior si ya era
// de rol "user". La API de Anthropic exige roles alternos: un mensaje de usuario
// inyectado a mitad de turno (tras los tool_result, que también son "user") debe
// ir en el mismo bloque, no como mensaje consecutivo.
func appendUser(out []map[string]any, content any) []map[string]any {
	if len(out) > 0 && out[len(out)-1]["role"] == "user" {
		prev := out[len(out)-1]
		merged := userBlocks(prev["content"])
		merged = append(merged, userBlocks(content)...)
		prev["content"] = merged
		return out
	}
	return append(out, map[string]any{"role": "user", "content": content})
}

// userBlocks normaliza el contenido de un mensaje de usuario a bloques.
func userBlocks(content any) []map[string]any {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []map[string]any{{"type": "text", "text": c}}
	case []map[string]any:
		return c
	}
	return nil
}

// cacheableBlockTypes son los tipos de bloque que aceptan cache_control.
var cacheableBlockTypes = map[string]bool{
	"text": true, "tool_use": true, "tool_result": true, "image": true, "document": true,
}

// addCacheBreakpoints coloca hasta 2 breakpoints de caché al final de la
// conversación (en los dos últimos mensajes), de modo que el prefijo estable
// (system + tools + historial) se reutilice entre pasos del bucle. Con system y
// tools ya hay 2 breakpoints; el límite de Anthropic es 4 por petición.
func addCacheBreakpoints(msgs []map[string]any) {
	added := 0
	for i := len(msgs) - 1; i >= 0 && added < 2; i-- {
		if markLastCacheableBlock(msgs[i]) {
			added++
		}
	}
}

// markLastCacheableBlock añade cache_control al último bloque cacheable del
// mensaje. Devuelve false si el mensaje no tiene contenido cacheable.
func markLastCacheableBlock(m map[string]any) bool {
	blocks, ok := normalizeBlocks(m["content"])
	if !ok {
		return false
	}
	m["content"] = blocks
	for i := len(blocks) - 1; i >= 0; i-- {
		typ, _ := blocks[i]["type"].(string)
		if cacheableBlockTypes[typ] {
			blocks[i]["cache_control"] = map[string]any{"type": "ephemeral"}
			return true
		}
	}
	return false
}

// normalizeBlocks convierte el content de un mensaje (string o arrays de JSON)
// a []map[string]any para poder inspeccionarlo y mutarlo.
func normalizeBlocks(content any) ([]map[string]any, bool) {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil, false
		}
		return []map[string]any{{"type": "text", "text": c}}, true
	case []map[string]any:
		return c, len(c) > 0
	case []any:
		out := make([]map[string]any, 0, len(c))
		for _, v := range c {
			b, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, b)
		}
		return out, len(out) > 0
	}
	return nil, false
}

// maxRawArgBytes limita las cadenas dentro de los inputs crudos de tool_use que
// se conservan en turnos ya cerrados (p. ej. el contenido de write_file).
const maxRawArgBytes = 800

// TrimHistory implementa provider.HistoryTrimmer para Anthropic.
//
// Reglas de la API (thinking con tool use):
//   - Dentro del turno de tool-use EN CURSO (i >= keepFrom) hay que devolver
//     todos los bloques de thinking: no se pueden omitir ni reordenar.
//   - Fuera de ese turno (turnos ya cerrados) se pueden omitir.
//
// Además, para todos los turnos salvo la assistant MÁS RECIENTE, sustituye por
// un marcador los inputs enormes de tool_use (contenido de write_file,
// old_string de edit_file…): el bloque tool_use se conserva (necesario para
// emparejar tool_result) pero deja de reenviarse el contenido completo.
// Si un turno cerrado del asistente no tiene tool_use, descarta ProviderRaw
// entero: basta con Content.
func (a *Anthropic) TrimHistory(msgs []model.Message, keepFrom int) []model.Message {
	out := make([]model.Message, len(msgs))
	copy(out, msgs)
	if keepFrom < 0 {
		keepFrom = 0
	}
	if keepFrom > len(out) {
		keepFrom = len(out)
	}
	lastAssistant := -1
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == model.RoleAssistant {
			lastAssistant = i
			break
		}
	}
	for i := range out {
		if out[i].Role != model.RoleAssistant || len(out[i].ProviderRaw) == 0 {
			continue
		}
		inActiveTurn := i >= keepFrom
		if !inActiveTurn && len(out[i].ToolCalls) == 0 {
			out[i].ProviderRaw = nil
			continue
		}
		var blocks []map[string]any
		if json.Unmarshal(out[i].ProviderRaw, &blocks) != nil {
			continue
		}
		kept := make([]map[string]any, 0, len(blocks))
		for _, b := range blocks {
			typ, _ := b["type"].(string)
			if !inActiveTurn && (typ == "thinking" || typ == "redacted_thinking") {
				continue
			}
			// El input completo solo se conserva en la assistant MÁS RECIENTE del
			// turno activo; en turnos cerrados (o en pasos anteriores del mismo
			// turno) ya no hace falta el contenido íntegro.
			if typ == "tool_use" && (!inActiveTurn || i != lastAssistant) {
				if input, ok := b["input"]; ok {
					b["input"], _ = model.ShrinkStrings(input, maxRawArgBytes)
				}
			}
			kept = append(kept, b)
		}
		if nb, err := json.Marshal(kept); err == nil {
			out[i].ProviderRaw = nb
		}
	}
	return out
}

// --- parsing del stream SSE ---

type blockAcc struct {
	typ       string
	text      string
	thinking  string
	signature string
	data      string // redacted_thinking
	toolID    string
	toolName  string
	input     strings.Builder
}

func parseSSE(r io.Reader, onDelta, onThinking DeltaFunc) (model.Message, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var blocks []*blockAcc
	stopReason := ""
	usage := &model.Usage{}

	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // ignora líneas "event:" y separadores en blanco
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var ev struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
				Data string `json:"data"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Message struct {
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		if ev.Message.Usage.InputTokens > 0 {
			usage.InputTokens = ev.Message.Usage.InputTokens
		}
		if ev.Usage.OutputTokens > 0 {
			usage.OutputTokens = ev.Usage.OutputTokens
		}

		switch ev.Type {
		case "content_block_start":
			for len(blocks) <= ev.Index {
				blocks = append(blocks, &blockAcc{})
			}
			b := blocks[ev.Index]
			b.typ = ev.ContentBlock.Type
			b.toolID = ev.ContentBlock.ID
			b.toolName = ev.ContentBlock.Name
			b.data = ev.ContentBlock.Data
		case "content_block_delta":
			if ev.Index >= len(blocks) {
				continue
			}
			b := blocks[ev.Index]
			switch ev.Delta.Type {
			case "text_delta":
				b.text += ev.Delta.Text
				if onDelta != nil {
					onDelta(ev.Delta.Text)
				}
			case "thinking_delta":
				b.thinking += ev.Delta.Thinking
				if onThinking != nil {
					onThinking(ev.Delta.Thinking)
				}
			case "signature_delta":
				b.signature += ev.Delta.Signature
			case "input_json_delta":
				b.input.WriteString(ev.Delta.PartialJSON)
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
		case "error":
			if ev.Error != nil {
				return model.Message{}, fmt.Errorf("anthropic stream error: %s", ev.Error.Message)
			}
		case "message_stop":
			// fin
		}
	}
	if err := sc.Err(); err != nil {
		return model.Message{}, err
	}

	return assemble(blocks, stopReason, usage)
}

// assemble reconstruye el Message neutral + ProviderRaw (los bloques crudos para
// el echo-back, con las firmas de thinking intactas).
func assemble(blocks []*blockAcc, stopReason string, usage *model.Usage) (model.Message, error) {
	out := model.Message{Role: model.RoleAssistant, StopReason: stopReason}
	var text strings.Builder
	raw := make([]map[string]any, 0, len(blocks))

	for _, b := range blocks {
		switch b.typ {
		case "text":
			text.WriteString(b.text)
			raw = append(raw, map[string]any{"type": "text", "text": b.text})
		case "thinking":
			raw = append(raw, map[string]any{"type": "thinking", "thinking": b.thinking, "signature": b.signature})
		case "redacted_thinking":
			raw = append(raw, map[string]any{"type": "redacted_thinking", "data": b.data})
		case "tool_use":
			var input any = map[string]any{}
			if s := b.input.String(); s != "" {
				_ = json.Unmarshal([]byte(s), &input)
			}
			argMap, _ := input.(map[string]any)
			if argMap == nil {
				argMap = map[string]any{}
			}
			out.ToolCalls = append(out.ToolCalls, model.ToolCall{ID: b.toolID, Name: b.toolName, Arguments: argMap})
			raw = append(raw, map[string]any{"type": "tool_use", "id": b.toolID, "name": b.toolName, "input": input})
		}
	}
	out.Content = text.String()
	out.Usage = usage
	if rawJSON, err := json.Marshal(raw); err == nil {
		out.ProviderRaw = rawJSON
	}
	return out, nil
}
