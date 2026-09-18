// Package tasks mantiene una lista de tareas persistente por directorio
// (.goolem/tasks.json). El agente la actualiza con task_write y la consulta con
// task_list; las tareas abiertas se inyectan en el system prompt cada turno, de
// modo que puede retomar trabajos de varios pasos entre sesiones.
package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/securefs"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
)

// Estados válidos.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
)

type Task struct {
	Title  string `json:"title"`
	Status string `json:"status"`
}

// Store persiste las tareas en <root>/.goolem/tasks.json. root se resuelve en
// cada llamada (rootFn) para seguir al directorio de trabajo móvil.
type Store struct {
	rootFn func() string
}

func New(rootFn func() string) *Store { return &Store{rootFn: rootFn} }

func (s *Store) path() string {
	return filepath.Join(s.rootFn(), ".goolem", "tasks.json")
}

func (s *Store) Load() []Task {
	data, err := os.ReadFile(s.path())
	if err != nil {
		return nil
	}
	var ts []Task
	if json.Unmarshal(data, &ts) != nil {
		return nil
	}
	return ts
}

func (s *Store) Save(ts []Task) error {
	p := s.path()
	data, _ := json.MarshalIndent(ts, "", "  ")
	return securefs.WriteFile(p, data)
}

func mark(status string) string {
	switch status {
	case StatusInProgress:
		return "in progress"
	case StatusDone:
		return "done"
	default:
		return "pending"
	}
}

// Render lista todas las tareas con su estado (para task_list).
func (s *Store) Render() string {
	ts := s.Load()
	if len(ts) == 0 {
		return "No tasks registered."
	}
	var b strings.Builder
	for _, t := range ts {
		fmt.Fprintf(&b, "- [%s] %s\n", mark(t.Status), t.Title)
	}
	return strings.TrimRight(b.String(), "\n")
}

// OpenSummary devuelve las tareas no terminadas para el system prompt ("" si no
// hay ninguna abierta).
func (s *Store) OpenSummary() string {
	ts := s.Load()
	var lines []string
	for _, t := range ts {
		if t.Status != StatusDone {
			lines = append(lines, fmt.Sprintf("- [%s] %s", mark(t.Status), t.Title))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "ACTIVE TASKS (resume them; update with task_write as you progress):\n" + strings.Join(lines, "\n")
}

// RegisterTools registra task_list y task_write.
func RegisterTools(reg *tools.Registry, s *Store) {
	reg.Register(model.ToolDefinition{
		Name:        "task_list",
		Description: "List active tasks with their status.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Mutating:    false,
	}, func(_ context.Context, _ map[string]any) (string, error) {
		return s.Render(), nil
	})

	reg.Register(model.ToolDefinition{
		Name: "task_write",
		Description: "Create or update the COMPLETE task list (replaces the previous one). " +
			"Use it to plan multi-step work and track progress: " +
			"set tasks to 'in_progress' when starting and 'done' when finished. Persists across sessions.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "Lista completa de tareas en orden",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title":  map[string]any{"type": "string", "description": "Task description"},
							"status": map[string]any{"type": "string", "enum": []string{StatusPending, StatusInProgress, StatusDone}, "description": "State (pending/in_progress/done)"},
						},
						"required": []string{"title", "status"},
					},
				},
			},
			"required": []string{"tasks"},
		},
		Mutating: false, // memoria de trabajo del agente, confinada a .goolem
	}, func(_ context.Context, args map[string]any) (string, error) {
		raw, ok := args["tasks"].([]any)
		if !ok {
			return "", fmt.Errorf("expected 'tasks' as a list")
		}
		var ts []Task
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			title, _ := m["title"].(string)
			status, _ := m["status"].(string)
			if strings.TrimSpace(title) == "" {
				continue
			}
			switch status {
			case StatusPending, StatusInProgress, StatusDone:
			default:
				status = StatusPending
			}
			ts = append(ts, Task{Title: title, Status: status})
		}
		if err := s.Save(ts); err != nil {
			return "", err
		}
		return "Tasks saved (" + fmt.Sprint(len(ts)) + "):\n" + s.Render(), nil
	})
}
