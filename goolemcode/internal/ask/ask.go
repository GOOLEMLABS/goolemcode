// Package ask registra la herramienta ask_model: consultar a OTRO modelo para
// una segunda opinión, sea cual sea el cerebro principal. Descubre los destinos
// disponibles según el entorno (DeepSeek/Claude si hay clave) y el servidor
// Ollama (enumera sus modelos vía /api/tags). Generaliza el antiguo query_deepseek.
package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/config"
	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/provider"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
	"github.com/GOOLEMLABS/goolemcode/internal/usage"
)

// RegisterTool registra ask_model con el catálogo de modelos ya construido
// (ver Build). No hace nada si no hay modelos consultables.
func RegisterTool(reg *tools.Registry, models map[string]provider.Provider, names []string, tracker *usage.Tracker) {
	if len(names) == 0 {
		return
	}
	reg.Register(model.ToolDefinition{
		Name: "ask_model",
		Description: "Query ANOTHER model for a second opinion and return its response. " +
			"Use it when the user asks to consult a specific model (e.g. \"ask deepseek…\") " +
			"or when you want a second opinion. Available models: " + strings.Join(names, ", ") + ".",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model":  map[string]any{"type": "string", "enum": names, "description": "Which model to query"},
				"prompt": map[string]any{"type": "string", "description": "The question or instruction for that model"},
			},
			"required": []string{"model", "prompt"},
		},
		Mutating: false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		name, _ := args["model"].(string)
		prompt, _ := args["prompt"].(string)
		p, ok := models[name]
		if !ok {
			return fmt.Sprintf("Model not available: %q. Available: %s", name, strings.Join(names, ", ")), nil
		}
		if strings.TrimSpace(prompt) == "" {
			return "", fmt.Errorf("prompt vacío")
		}
		msgs := []model.Message{{Role: model.RoleUser, Content: prompt}}
		resp, err := p.Chat(ctx, msgs, nil, "", nil, nil)
		if err != nil {
			return "", err
		}
		if resp.Usage != nil {
			tracker.Add(p.Label(), *resp.Usage)
		}
		return resp.Content, nil
	})
}

// Build reúne los proveedores consultables (deepseek/claude si hay clave, y los
// modelos del servidor Ollama) y sus nombres en orden estable. Lo comparten
// ask_model y el comando /model, y hace una sola llamada a /api/tags.
func Build(cfg config.Config) (map[string]provider.Provider, []string) {
	m := map[string]provider.Provider{}
	var names []string
	add := func(name string, p provider.Provider) {
		if _, exists := m[name]; exists {
			return
		}
		m[name] = p
		names = append(names, name)
	}

	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		add("deepseek", provider.NewDeepSeek(key, cfg.DeepSeekModel))
		add("deepseek-v4-pro", provider.NewDeepSeek(key, "deepseek-v4-pro"))
		add("deepseek-v4-flash", provider.NewDeepSeek(key, "deepseek-v4-flash"))
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		add("claude", provider.NewAnthropic("claude-opus-4-8", cfg.MaxTokens, cfg.Effort))
	}
	// Modelos del servidor Ollama (si responde /api/tags). Nombre = el del modelo.
	for _, mdl := range listOllamaModels(cfg.OllamaURL) {
		add(mdl, provider.NewOllama(cfg.OllamaURL, mdl))
	}
	return m, names
}

// listOllamaModels enumera los modelos del servidor (vacío si no responde).
func listOllamaModels(baseURL string) []string {
	if baseURL == "" {
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/tags", nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil
	}
	var names []string
	for _, m := range body.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	sort.Strings(names)
	return names
}
