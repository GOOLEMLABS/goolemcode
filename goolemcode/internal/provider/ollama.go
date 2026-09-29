package provider

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

// Ollama habla con el endpoint /api/chat en modo STREAMING (JSON por líneas).
// qwen3 y compañía emiten tool_calls también en streaming, así que acumulamos
// texto (emitido por onDelta) y tool_calls, y leemos el uso en el mensaje final.
type Ollama struct {
	BaseURL string
	Model   string
	client  *http.Client
	noTools bool // el modelo no soporta tool-calling (p. ej. los VL); se aprende del 400
}

func NewOllama(baseURL, modelID string) *Ollama {
	return &Ollama{BaseURL: strings.TrimRight(baseURL, "/"), Model: modelID, client: &http.Client{Timeout: 10 * time.Minute}}
}

func (o *Ollama) Label() string { return fmt.Sprintf("Ollama (%s)", o.Model) }

type ollamaFunc struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}
type ollamaToolCall struct {
	Function ollamaFunc `json:"function"`
}
type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Thinking  string           `json:"thinking,omitempty"` // qwen3 emite razonamiento aquí
	Images    []string         `json:"images,omitempty"`   // base64 (modelos con visión)
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}
type ollamaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}
type ollamaReq struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
}
type ollamaStreamChunk struct {
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
}

func (o *Ollama) Chat(ctx context.Context, messages []model.Message, tools []model.ToolDefinition, system string, onDelta, onThinking DeltaFunc) (model.Message, error) {
	req := ollamaReq{Model: o.Model, Stream: true}
	if system != "" {
		req.Messages = append(req.Messages, ollamaMessage{Role: "system", Content: system})
	}
	if o.noTools {
		req.Messages = append(req.Messages, ollamaMessage{
			Role:    "system",
			Content: "NOTE: This model does not support tool calling. You cannot use tools. Respond with text or code blocks directly.",
		})
	}
	for _, m := range messages {
		switch m.Role {
		case model.RoleUser:
			um := ollamaMessage{Role: "user", Content: m.Content}
			for _, img := range m.Images {
				um.Images = append(um.Images, base64.StdEncoding.EncodeToString(img.Bytes))
			}
			req.Messages = append(req.Messages, um)
		case model.RoleSystem:
			req.Messages = append(req.Messages, ollamaMessage{Role: "system", Content: m.Content})
		case model.RoleAssistant:
			om := ollamaMessage{Role: "assistant", Content: m.Content}
			for _, c := range m.ToolCalls {
				om.ToolCalls = append(om.ToolCalls, ollamaToolCall{Function: ollamaFunc{Name: c.Name, Arguments: c.Arguments}})
			}
			req.Messages = append(req.Messages, om)
		case model.RoleTool:
			for _, r := range m.ToolResults {
				req.Messages = append(req.Messages, ollamaMessage{Role: "tool", Content: r.Content})
			}
		}
	}
	var toolDefs []ollamaTool
	for _, t := range tools {
		var ot ollamaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.InputSchema
		toolDefs = append(toolDefs, ot)
	}

	headers := map[string]string{"Content-Type": "application/json"}
	send := func(withTools bool) (*http.Response, error) {
		r := req
		if withTools {
			r.Tools = toolDefs
		}
		body, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		return doWithRetry(ctx, o.client, http.MethodPost, o.BaseURL+"/api/chat", headers, body, 3)
	}

	resp, err := send(!o.noTools)
	if err != nil {
		return model.Message{}, err
	}
	// Los modelos sin tool-calling (p. ej. los de visión) devuelven 400
	// "does not support tools": lo aprendemos y reintentamos sin herramientas.
	if resp.StatusCode == http.StatusBadRequest && !o.noTools {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if strings.Contains(string(b), "does not support tools") {
			o.noTools = true
			if resp, err = send(false); err != nil {
				return model.Message{}, err
			}
		} else {
			return model.Message{}, fmt.Errorf("ollama HTTP 400: %s", string(b))
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return model.Message{}, fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, string(b))
	}
	guard := newStallGuard(resp.Body, resp.Body.Close, StreamIdleTimeout)
	defer guard.stop()
	return parseOllamaStream(guard, onDelta, onThinking)
}

// parseOllamaStream ensambla el Message neutral desde el stream JSON-por-líneas
// de /api/chat. Acumula texto (emitido por onDelta), razonamiento (onThinking),
// tool_calls (con IDs sintéticos, Ollama no siempre los da) y el uso del chunk
// final. Puro → testeable.
func parseOllamaStream(r io.Reader, onDelta, onThinking DeltaFunc) (model.Message, error) {
	out := model.Message{Role: model.RoleAssistant}
	var content strings.Builder
	var calls []model.ToolCall
	usage := &model.Usage{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var chunk ollamaStreamChunk
		if json.Unmarshal([]byte(line), &chunk) != nil {
			continue
		}
		if d := chunk.Message.Thinking; d != "" && onThinking != nil {
			onThinking(d)
		}
		if d := chunk.Message.Content; d != "" {
			content.WriteString(d)
			if onDelta != nil {
				onDelta(d)
			}
		}
		for i, tc := range chunk.Message.ToolCalls {
			// Ollama no siempre asigna IDs; sintetizamos uno estable por posición.
			calls = append(calls, model.ToolCall{
				ID:        fmt.Sprintf("call_%d", len(calls)+i),
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		if chunk.Done {
			usage.InputTokens = chunk.PromptEvalCount
			usage.OutputTokens = chunk.EvalCount
		}
	}
	if err := sc.Err(); err != nil {
		return model.Message{}, err
	}

	out.Content = content.String()
	out.ToolCalls = calls
	out.Usage = usage
	if len(calls) > 0 {
		out.StopReason = "tool_use"
	} else {
		out.StopReason = "end_turn"
	}
	return out, nil
}
