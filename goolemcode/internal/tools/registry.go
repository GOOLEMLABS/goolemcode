// Package tools unites local and MCP tools into a single registry.
// The agent only talks to this registry.
package tools

import (
	"context"
	"fmt"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

type Handler func(ctx context.Context, args map[string]any) (string, error)

type Registry struct {
	defs     map[string]model.ToolDefinition
	handlers map[string]Handler
	order    []string
	disabled map[string]bool // tools excluded from Definitions()
	// Optional hooks for the tool lifecycle (see hooks package).
	preHook  func(name string, args map[string]any) (block bool, reason string)
	postHook func(name string, args map[string]any, result string, isErr bool)
}

func NewRegistry() *Registry {
	return &Registry{defs: map[string]model.ToolDefinition{}, handlers: map[string]Handler{}, disabled: map[string]bool{}}
}

// SetHooks registers Pre/PostToolUse hooks (nil to disable). Since they live in
// the registry, they also apply to sub-agents (shared registry).
func (r *Registry) SetHooks(
	pre func(name string, args map[string]any) (bool, string),
	post func(name string, args map[string]any, result string, isErr bool),
) {
	r.preHook, r.postHook = pre, post
}

func (r *Registry) Register(def model.ToolDefinition, h Handler) {
	if _, ok := r.defs[def.Name]; ok {
		return
	}
	r.defs[def.Name] = def
	r.handlers[def.Name] = h
	r.order = append(r.order, def.Name)
}

// Disable marks a tool as disabled so it won't appear in Definitions().
// The handler remains registered but the model won't see it.
func (r *Registry) Disable(name string) { r.disabled[name] = true }

// Enable removes a disabled mark.
func (r *Registry) Enable(name string) { delete(r.disabled, name) }

// Disabled returns the set of disabled tool names.
func (r *Registry) Disabled() []string {
	var out []string
	for n := range r.disabled {
		out = append(out, n)
	}
	return out
}

func (r *Registry) Definitions() []model.ToolDefinition {
	out := make([]model.ToolDefinition, 0, len(r.order))
	for _, n := range r.order {
		d := r.defs[n]
		if d.Internal || r.disabled[n] {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (r *Registry) Names() []string {
	var out []string
	for _, n := range r.order {
		d := r.defs[n]
		if d.Internal || r.disabled[n] {
			continue
		}
		out = append(out, n)
	}
	return out
}

func (r *Registry) IsMutating(name string) bool {
	if d, ok := r.defs[name]; ok {
		return d.Mutating
	}
	return true // unknown → treat as dangerous
}

func (r *Registry) Execute(ctx context.Context, call model.ToolCall) model.ToolResult {
	h, ok := r.handlers[call.Name]
	if !ok {
		return model.ToolResult{CallID: call.ID, Content: "Unknown tool: " + call.Name, IsError: true}
	}
	if r.preHook != nil { // PreToolUse can veto execution
		if block, reason := r.preHook(call.Name, call.Arguments); block {
			return model.ToolResult{CallID: call.ID, Content: "Blocked by PreToolUse hook: " + reason, IsError: true}
		}
	}
	res := model.ToolResult{CallID: call.ID}
	out, err := h(ctx, call.Arguments)
	if err != nil {
		res.Content, res.IsError = fmt.Sprintf("Error in %s: %v", call.Name, err), true
	} else if out == "" {
		res.Content = "(no output)"
	} else {
		res.Content = out
	}
	if r.postHook != nil { // PostToolUse: side effects (formatting, notifying…)
		r.postHook(call.Name, call.Arguments, res.Content, res.IsError)
	}
	return res
}
