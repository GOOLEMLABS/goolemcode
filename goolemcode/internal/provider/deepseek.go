package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

const deepSeekEndpoint = "https://api.deepseek.com/v1/chat/completions"

// DeepSeek implementa el endpoint OpenAI-compatible de DeepSeek, con STREAMING
// SSE. La API key se pasa al constructor (leída de DEEPSEEK_API_KEY en main).
type DeepSeek struct {
	apiKey string
	model  string
	client *http.Client
}

func NewDeepSeek(apiKey, modelID string) *DeepSeek {
	return &DeepSeek{apiKey: apiKey, model: modelID, client: &http.Client{Timeout: 10 * time.Minute}}
}

func (d *DeepSeek) Label() string { return fmt.Sprintf("DeepSeek (%s)", d.model) }

func dsStrPtr(s string) *string { return &s }

// dsMessage usa *string para Content: nil → JSON null (necesario cuando hay
// tool_calls en el turno del asistente, que OpenAI-compat espera content:null).
type dsMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"` // nil → null
	ToolCalls  []dsToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type dsToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function dsFunc `json:"function"`
}

type dsFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string (formato OpenAI)
}

type dsTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type dsStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}
type dsReq struct {
	Model         string           `json:"model"`
	Messages      []dsMessage      `json:"messages"`
	Tools         []dsTool         `json:"tools,omitempty"`
	Stream        bool             `json:"stream"`
	StreamOptions *dsStreamOptions `json:"stream_options,omitempty"`
}

// dsStreamChunk es un evento SSE del stream (formato OpenAI). Los tool_calls
// llegan por fragmentos: index identifica cuál, y arguments se concatena.
type dsStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"` // deepseek-reasoner
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// dsBuildMessages traduce el historial neutral al formato OpenAI de DeepSeek.
// Clave: el turno del asistente con tool_calls lleva content:null (nil *string),
// que es lo que la API espera. Puro → testeable sin red.
func dsBuildMessages(system string, messages []model.Message) []dsMessage {
	var out []dsMessage
	if system != "" {
		out = append(out, dsMessage{Role: "system", Content: dsStrPtr(system)})
	}
	for _, m := range messages {
		switch m.Role {
		case model.RoleUser:
			out = append(out, dsMessage{Role: "user", Content: dsStrPtr(m.Content)})
		case model.RoleSystem:
			out = append(out, dsMessage{Role: "system", Content: dsStrPtr(m.Content)})
		case model.RoleAssistant:
			dm := dsMessage{Role: "assistant"}
			if len(m.ToolCalls) > 0 {
				// content null cuando el asistente llama herramientas
				for _, tc := range m.ToolCalls {
					args, _ := json.Marshal(tc.Arguments)
					dm.ToolCalls = append(dm.ToolCalls, dsToolCall{
						ID: tc.ID, Type: "function",
						Function: dsFunc{Name: tc.Name, Arguments: string(args)},
					})
				}
			} else {
				dm.Content = dsStrPtr(m.Content)
			}
			out = append(out, dm)
		case model.RoleTool:
			for _, r := range m.ToolResults {
				out = append(out, dsMessage{
					Role: "tool", Content: dsStrPtr(r.Content), ToolCallID: r.CallID,
				})
			}
		}
	}
	return out
}

func (d *DeepSeek) Chat(ctx context.Context, messages []model.Message, toolDefs []model.ToolDefinition, system string, onDelta, onThinking DeltaFunc) (model.Message, error) {
	req := dsReq{Model: d.model, Stream: true, StreamOptions: &dsStreamOptions{IncludeUsage: true}}
	req.Messages = dsBuildMessages(system, messages)
	for _, t := range toolDefs {
		var dt dsTool
		dt.Type = "function"
		dt.Function.Name = t.Name
		dt.Function.Description = t.Description
		dt.Function.Parameters = t.InputSchema
		req.Tools = append(req.Tools, dt)
	}

	body, _ := json.Marshal(req)
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + d.apiKey,
	}
	resp, err := doWithRetry(ctx, d.client, http.MethodPost, deepSeekEndpoint, headers, body, 3)
	if err != nil {
		return model.Message{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := readAllLimited(resp.Body)
		return model.Message{}, fmt.Errorf("deepseek HTTP %d: %s", resp.StatusCode, b)
	}
	guard := newStallGuard(resp.Body, resp.Body.Close, StreamIdleTimeout)
	defer guard.stop()
	return parseDeepSeekStream(guard, onDelta, onThinking)
}

// parseDeepSeekStream ensambla el Message neutral desde el stream SSE (formato
// OpenAI). Los tool_calls llegan troceados: index identifica cada uno y los
// fragmentos de arguments se concatenan en orden. reasoning_content (si el modelo
// razona) se emite por onThinking. Puro (io.Reader) → testeable.
func parseDeepSeekStream(r io.Reader, onDelta, onThinking DeltaFunc) (model.Message, error) {
	type acc struct {
		id, name string
		args     strings.Builder
	}
	toolAccs := map[int]*acc{}
	var order []int
	var content strings.Builder
	finish := ""
	usage := &model.Usage{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk dsStreamChunk
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Error != nil {
			return model.Message{}, fmt.Errorf("deepseek: %s", chunk.Error.Message)
		}
		if u := chunk.Usage; u != nil {
			usage.InputTokens = u.PromptTokens
			usage.OutputTokens = u.CompletionTokens
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.ReasoningContent != "" && onThinking != nil {
			onThinking(ch.Delta.ReasoningContent)
		}
		if ch.Delta.Content != "" {
			content.WriteString(ch.Delta.Content)
			if onDelta != nil {
				onDelta(ch.Delta.Content)
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			a := toolAccs[tc.Index]
			if a == nil {
				a = &acc{}
				toolAccs[tc.Index] = a
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				a.id = tc.ID
			}
			if tc.Function.Name != "" {
				a.name = tc.Function.Name
			}
			a.args.WriteString(tc.Function.Arguments)
		}
		if ch.FinishReason != "" {
			finish = ch.FinishReason
		}
	}
	if err := sc.Err(); err != nil {
		return model.Message{}, err
	}

	out := model.Message{Role: model.RoleAssistant, Content: content.String(), Usage: usage}
	for i, idx := range order {
		a := toolAccs[idx]
		var args map[string]any
		_ = json.Unmarshal([]byte(a.args.String()), &args)
		id := a.id
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		out.ToolCalls = append(out.ToolCalls, model.ToolCall{ID: id, Name: a.name, Arguments: args})
	}
	if finish == "tool_calls" || len(out.ToolCalls) > 0 {
		out.StopReason = "tool_use"
	} else {
		out.StopReason = "end_turn"
	}
	return out, nil
}

func readAllLimited(r io.Reader) (string, error) {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	return string(b), nil
}
