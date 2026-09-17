package mcp

import (
	"context"
	"fmt"
	"regexp"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
)

var nameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// Manager conecta a los servidores MCP y registra sus herramientas en el mismo
// Registry que las locales (unificación dinámica). Los nombres se prefijan con
// el del servidor para evitar colisiones.
type Manager struct {
	clients   []*Client
	Connected []string
}

func (m *Manager) ConnectAll(ctx context.Context, servers []ServerSpec, reg *tools.Registry) {
	for _, s := range servers {
		c := NewClient(s)
		if err := c.Start(ctx); err != nil {
			fmt.Printf("  ⚠ MCP '%s': %v\n", s.Name, err)
			continue
		}
		list, err := c.ListTools()
		if err != nil {
			fmt.Printf("  ⚠ MCP '%s' tools/list: %v\n", s.Name, err)
			c.Stop()
			continue
		}
		prefix := sanitize(s.Name)
		for _, t := range list {
			tool := t
			client := c
			schema := tool.InputSchema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			reg.Register(model.ToolDefinition{
				Name:        prefix + "__" + sanitize(tool.Name),
				Description: tool.Description,
				InputSchema: schema,
				Mutating:    true, // efectos desconocidos → confirmar + checkpoint
			}, func(_ context.Context, args map[string]any) (string, error) {
				text, isErr, err := client.CallTool(tool.Name, args)
				if err != nil {
					return "", err
				}
				if isErr {
					return "(error MCP)\n" + text, nil
				}
				return text, nil
			})
		}
		m.clients = append(m.clients, c)
		m.Connected = append(m.Connected, s.Name)
	}
}

func (m *Manager) Shutdown() {
	for _, c := range m.clients {
		c.Stop()
	}
}

func sanitize(s string) string {
	out := nameRe.ReplaceAllString(s, "_")
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}
