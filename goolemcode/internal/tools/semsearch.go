package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float64 `json:"embeddings"`
}

const embedModel = "nomic-embed-text"

func RegisterSemanticSearch(reg *Registry, ollamaURL string) {
	base := strings.TrimRight(ollamaURL, "/")
	client := &http.Client{Timeout: 60 * time.Second}

	embed := func(texts []string) ([][]float64, error) {
		body, _ := json.Marshal(embedRequest{Model: embedModel, Input: texts})
		resp, err := client.Post(base+"/api/embed", "application/json", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("could not connect to Ollama: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, fmt.Errorf("Ollama /api/embed HTTP %d: %s", resp.StatusCode, string(b))
		}
		var er embedResponse
		if json.NewDecoder(resp.Body).Decode(&er) != nil {
			return nil, fmt.Errorf("invalid Ollama response")
		}
		return er.Embeddings, nil
	}

	cosineSim := func(a, b []float64) float64 {
		if len(a) != len(b) {
			return 0
		}
		var dot, na, nb float64
		for i := range a {
			dot += a[i] * b[i]
			na += a[i] * a[i]
			nb += b[i] * b[i]
		}
		if na == 0 || nb == 0 {
			return 0
		}
		return dot / (math.Sqrt(na) * math.Sqrt(nb))
	}

	readTextChunks := func(basePath string, maxBytes int) ([]string, error) {
		info, err := os.Stat(basePath)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			data, err := os.ReadFile(basePath)
			if err != nil {
				return nil, err
			}
			if len(data) > maxBytes {
				data = data[:maxBytes]
			}
			return []string{string(data)}, nil
		}

		var chunks []string
		_ = filepath.WalkDir(basePath, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if treeExclude[d.Name()] {
				return nil
			}
			if len(chunks) >= 50 {
				return filepath.SkipAll
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			if len(data) > maxBytes {
				data = data[:maxBytes]
			}
			rel, _ := filepath.Rel(basePath, p)
			chunks = append(chunks, "--- "+rel+" ---\n"+string(data))
			return nil
		})
		return chunks, nil
	}

	reg.Register(model.ToolDefinition{
		Name:        "semantic_search",
		Description: "Search by meaning (semantic similarity) using Ollama embeddings. Scans local files and ranks them by relevance to the query.",
		InputSchema: obj(map[string]any{
			"query":       prop("string", "Natural language query"),
			"path":        prop("string", "Path of file or directory to search (relative or absolute)"),
			"max_results": prop("integer", "Maximum results (default 10)"),
		}, "query"),
		Mutating: false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		query := str(args["query"])
		if strings.TrimSpace(query) == "" {
			return "", fmt.Errorf("query is required")
		}

		relPath := str(args["path"])
		basePath := "."
		if relPath != "" {
			basePath = relPath
		}

		maxResults := 10
		if v, ok := toInt(args["max_results"]); ok && v > 0 {
			maxResults = v
		}

		chunks, err := readTextChunks(basePath, 8000)
		if err != nil {
			return fmt.Sprintf("Error reading files: %s", err), nil
		}
		if len(chunks) == 0 {
			return "No files found to search.", nil
		}
		if len(chunks) == 1 && chunks[0] == "" {
			return "No files found to search.", nil
		}

		qEmbed, err := embed([]string{query})
		if err != nil {
			return fmt.Sprintf("Error generating query embedding: %s", err), nil
		}

		type hit struct {
			text  string
			score float64
		}

		batchSize := 10
		var hits []hit
		for i := 0; i < len(chunks); i += batchSize {
			end := i + batchSize
			if end > len(chunks) {
				end = len(chunks)
			}
			embeds, err := embed(chunks[i:end])
			if err != nil {
				return fmt.Sprintf("Error generating embeddings: %s", err), nil
			}
			for j, emb := range embeds {
				score := cosineSim(qEmbed[0], emb)
				if score > 0.3 {
					hits = append(hits, hit{text: chunks[i+j], score: score})
				}
			}
		}

		sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
		if len(hits) > maxResults {
			hits = hits[:maxResults]
		}

		if len(hits) == 0 {
			return fmt.Sprintf("No results for: %s (scanned %d files)", query, len(chunks)), nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Results for: %s\n(from %d files, %d matches)\n\n", query, len(chunks), len(hits))
		for i, h := range hits {
			short := strings.ReplaceAll(h.text, "\n", " ")
			if len(short) > 300 {
				short = short[:300] + "…"
			}
			fmt.Fprintf(&b, "%d. [%.3f] %s\n\n", i+1, h.score, short)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	})
}
