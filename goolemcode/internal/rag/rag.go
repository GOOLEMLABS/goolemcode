// Package rag expone la herramienta `rag_search`, que consulta el servicio
// goolem-rag (RAG de documentos personales, FastAPI sobre ChromaDB +
// nomic-embed-text en el host GOOLEM, :8003).
//
// Endpoint: GET /search?q=<consulta>&user=<usuario>&n=<k>
// Respuesta: {user, query, results:[{text, file_name, file_path, score}]}
//
// Es solo lectura → Mutating=false (no pide confirmación). El `user` por defecto
// sale de la config; el modelo puede sobrescribirlo.
package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
)

const maxSnippet = 600

type result struct {
	Text     string  `json:"text"`
	FileName string  `json:"file_name"`
	FilePath string  `json:"file_path"`
	Score    float64 `json:"score"`
}

type searchResponse struct {
	User    string   `json:"user"`
	Query   string   `json:"query"`
	Results []result `json:"results"`
}

// RegisterTool registra `rag_search` apuntando al servicio goolem-rag.
func RegisterTool(reg *tools.Registry, baseURL, defaultUser string, defaultN int) {
	base := strings.TrimRight(baseURL, "/")
	if defaultN <= 0 {
		defaultN = 5
	}
	client := &http.Client{Timeout: 60 * time.Second}

	reg.Register(model.ToolDefinition{
		Name:        "rag_search",
		Description: "Search the user's personal documents (goolem-rag RAG) by semantic similarity.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Natural language query"},
				"n":     map[string]any{"type": "integer", "description": fmt.Sprintf("Number of results (default %d)", defaultN)},
				"user":  map[string]any{"type": "string", "description": fmt.Sprintf("User collection (default '%s')", defaultUser)},
			},
			"required": []string{"query"},
		},
		Mutating: false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		query := asString(args["query"])
		if strings.TrimSpace(query) == "" {
			return "", fmt.Errorf("query is empty")
		}
		user := asString(args["user"])
		if user == "" {
			user = defaultUser
		}
		n := defaultN
		if v, ok := args["n"]; ok {
			if iv := toInt(v); iv > 0 {
				n = iv
			}
		}

		q := url.Values{}
		q.Set("q", query)
		q.Set("user", user)
		q.Set("n", strconv.Itoa(n))
		reqURL := base + "/search?" + q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("goolem-rag not responding: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("goolem-rag HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var data searchResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return "", fmt.Errorf("invalid response from goolem-rag: %w", err)
		}
		if len(data.Results) == 0 {
			return fmt.Sprintf("No results for \"%s\" (user=%s).", query, user), nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "%d results for \"%s\" (user=%s):", len(data.Results), query, user)
		for i, r := range data.Results {
			path := r.FilePath
			if path == "" {
				path = r.FileName
			}
			if path == "" {
				path = "?"
			}
			text := strings.ReplaceAll(strings.TrimSpace(r.Text), "\n", " ")
			if len(text) > maxSnippet {
				text = text[:maxSnippet] + "…"
			}
			fmt.Fprintf(&b, "\n\n[%d] %s (score %.3f)\n%s", i+1, path, r.Score, text)
		}
		return b.String(), nil
	})
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	}
	return 0
}
