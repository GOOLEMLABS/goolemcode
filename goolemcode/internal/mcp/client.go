// Package mcp implementa un cliente MCP nativo (JSON-RPC 2.0) con varios
// transportes: stdio (proceso hijo), SSE legacy (GET /sse + POST) y streamable
// HTTP (POST único). Sin SDK → cero dependencias.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const requestTimeout = 120 * time.Second

type ServerSpec struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// Transporte HTTP/SSE (alternativa a Command):
	URL       string            `json:"url"`       // endpoint del servidor MCP
	Transport string            `json:"transport"` // "stdio" | "sse" | "http" (auto si vacío)
	Headers   map[string]string `json:"headers"`   // cabeceras extra (p. ej. Authorization)
}

type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type rpcResp struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// transport abstrae el canal de mensajes JSON-RPC. stdio y SSE son asíncronos
// (respuestas correlacionadas por id vía rpcMux); streamable HTTP es síncrono.
type transport interface {
	start(ctx context.Context) error
	request(method string, params any) (json.RawMessage, error)
	notify(method string, params any) error
	stop()
}

// rpcMux correlaciona respuestas asíncronas con sus peticiones por id.
type rpcMux struct {
	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResp
}

func newRPCMux() *rpcMux { return &rpcMux{pending: map[int]chan rpcResp{}} }

func (m *rpcMux) alloc() (int, chan rpcResp) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	ch := make(chan rpcResp, 1)
	m.pending[m.nextID] = ch
	return m.nextID, ch
}

func (m *rpcMux) dispatch(resp rpcResp) {
	m.mu.Lock()
	ch, ok := m.pending[resp.ID]
	if ok {
		delete(m.pending, resp.ID)
	}
	m.mu.Unlock()
	if ok {
		ch <- resp
	}
}

func (m *rpcMux) wait(ch chan rpcResp, method string) (json.RawMessage, error) {
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp %s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	case <-time.After(requestTimeout):
		return nil, fmt.Errorf("mcp %s: timeout", method)
	}
}

// Client es el cliente MCP de alto nivel; delega el transporte concreto.
type Client struct {
	spec ServerSpec
	t    transport
}

func NewClient(spec ServerSpec) *Client { return &Client{spec: spec} }

func (c *Client) Name() string { return c.spec.Name }

func (c *Client) Start(ctx context.Context) error {
	switch {
	case c.spec.Command != "":
		c.t = newStdioTransport(c.spec)
	case c.spec.URL != "":
		mode := c.spec.Transport
		if mode == "" {
			mode = "http" // streamable HTTP es el estándar actual
		}
		if mode == "sse" {
			c.t = newSSETransport(c.spec)
		} else {
			c.t = newHTTPTransport(c.spec)
		}
	default:
		return fmt.Errorf("servidor MCP '%s' sin 'command' ni 'url'", c.spec.Name)
	}

	if err := c.t.start(ctx); err != nil {
		return err
	}
	if _, err := c.t.request("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "goolemcode", "version": "0.1.0"},
	}); err != nil {
		return err
	}
	return c.t.notify("notifications/initialized", map[string]any{})
}

func (c *Client) ListTools() ([]ToolInfo, error) {
	raw, err := c.t.request("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

func (c *Client) CallTool(name string, args map[string]any) (string, bool, error) {
	raw, err := c.t.request("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", true, err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", true, err
	}
	var parts []string
	for _, p := range res.Content {
		if p.Type == "text" {
			parts = append(parts, p.Text)
		}
	}
	text := ""
	for i, p := range parts {
		if i > 0 {
			text += "\n"
		}
		text += p
	}
	if text == "" {
		text = "(sin salida)"
	}
	return text, res.IsError, nil
}

func (c *Client) Stop() {
	if c.t != nil {
		c.t.stop()
	}
}
