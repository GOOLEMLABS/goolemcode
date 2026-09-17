package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

// RegisterGit registers first-class git tools. status/diff/log are read-only;
// commit is a mutator (goes through checkpoint + confirmation).
func RegisterGit(reg *Registry, ws *Workspace) {
	run := func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = ws.Root()
		out, err := cmd.CombinedOutput()
		s := strings.TrimRight(string(out), "\n")
		if len(s) > maxOutput {
			s = s[:maxOutput] + "\n… (truncado)"
		}
		if err != nil {
			if _, ok := err.(*exec.ExitError); ok {
				return s, nil // git returned code != 0: its output is the result
			}
			return "", fmt.Errorf("git not available or failed: %w", err)
		}
		return s, nil
	}

	reg.Register(model.ToolDefinition{
		Name:        "git_status",
		Description: "Show Git repository status (git status -sb).",
		InputSchema: obj(nil),
		Mutating:    false,
	}, func(ctx context.Context, _ map[string]any) (string, error) {
		out, err := run(ctx, "status", "-sb")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			return "Clean working tree.", nil
		}
		return out, nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "git_diff",
		Description: "Show diff of uncommitted changes. Use staged:true for staged changes; 'path' to filter by file.",
		InputSchema: obj(map[string]any{
			"staged": prop("boolean", "Show staged changes (git diff --cached)"),
			"path":   prop("string", "Filter by file/directory (optional)"),
		}),
		Mutating: false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		a := []string{"diff"}
		if b, ok := args["staged"].(bool); ok && b {
			a = append(a, "--cached")
		}
		if p := str(args["path"]); p != "" {
			if _, err := safeJoin(ws.Root(), p); err != nil {
				return "", err
			}
			a = append(a, "--", p)
		}
		out, err := run(ctx, a...)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			return "No changes.", nil
		}
		return out, nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "git_log",
		Description: "Show commit history. Use --oneline for compact format, -N to limit entries.",
		InputSchema: obj(map[string]any{"n": prop("integer", "Number of commits (default 10)")}),
		Mutating:    false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		n := 10
		if v, ok := toInt(args["n"]); ok && v > 0 {
			n = v
		}
		return run(ctx, "log", "--oneline", "-n", fmt.Sprintf("%d", n))
	})

	reg.Register(model.ToolDefinition{
		Name:        "git_commit",
		Description: "Create a commit with tracked file changes (git commit -am). Use add_all:true to 'git add -A' first (includes new files).",
		InputSchema: obj(map[string]any{
			"message": prop("string", "Commit message"),
			"add_all": prop("boolean", "Run 'git add -A' first (includes new files)"),
		}, "message"),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		msg := str(args["message"])
		if strings.TrimSpace(msg) == "" {
			return "", fmt.Errorf("empty message")
		}
		if b, ok := args["add_all"].(bool); ok && b {
			if out, err := run(ctx, "add", "-A"); err != nil {
				return out, err
			}
		}
		return run(ctx, "commit", "-am", msg)
	})

	reg.Register(model.ToolDefinition{
		Name:        "git_push",
		Description: "Push commits to remote (git push). Use --force-with-lease for safety on diverged branches.",
		InputSchema: obj(map[string]any{
			"remote": prop("string", "Remote name (default: origin)"),
			"branch": prop("string", "Branch to push (default: current branch)"),
			"force":  prop("boolean", "Use --force-with-lease if push is rejected (use sparingly)"),
		}),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		a := []string{"push"}
		remote := str(args["remote"])
		if remote == "" {
			remote = "origin"
		}
		branch := str(args["branch"])
		if b, ok := args["force"].(bool); ok && b {
			a = append(a, "--force-with-lease")
		}
		a = append(a, remote)
		if branch != "" {
			a = append(a, branch)
		}
		return run(ctx, a...)
	})

	reg.Register(model.ToolDefinition{
		Name:        "git_branch",
		Description: "List, create, or delete branches. Pass name to create; --delete to remove.",
		InputSchema: obj(map[string]any{
			"name":   prop("string", "Branch name to create or delete"),
			"delete": prop("boolean", "Delete the branch instead of creating"),
		}),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		name := str(args["name"])
		if name == "" {
			return run(ctx, "branch")
		}
		if _, ok := args["delete"].(bool); ok && args["delete"].(bool) {
			return run(ctx, "branch", "-d", name)
		}
		return run(ctx, "branch", name)
	})

	reg.Register(model.ToolDefinition{
		Name:        "git_stash",
		Description: "Stash changes (push, pop, list, drop). Use save to stash, pop to restore, list to view.",
		InputSchema: obj(map[string]any{
			"action":  prop("string", "Action: save, pop, list, drop (default: save)"),
			"message": prop("string", "Optional stash message (only for save)"),
		}),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		action := str(args["action"])
		if action == "" {
			action = "save"
		}
		msg := str(args["message"])
		switch action {
		case "save":
			if msg != "" {
				return run(ctx, "stash", "push", "-m", msg)
			}
			return run(ctx, "stash", "push")
		case "pop":
			return run(ctx, "stash", "pop")
		case "list":
			return run(ctx, "stash", "list")
		case "drop":
			return run(ctx, "stash", "drop")
		default:
			return "", fmt.Errorf("unknown stash action: %s (use save, pop, list, drop)", action)
		}
	})

	reg.Register(model.ToolDefinition{
		Name:        "git_merge",
		Description: "Merge a branch into the current branch. Handles conflicts that require resolution.",
		InputSchema: obj(map[string]any{
			"branch": prop("string", "Branch name to merge into current"),
			"no_ff":  prop("boolean", "Create a merge commit even when fast-forward is possible"),
		}, "branch"),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		branch := str(args["branch"])
		if branch == "" {
			return "", fmt.Errorf("branch name required")
		}
		a := []string{"merge"}
		if b, ok := args["no_ff"].(bool); ok && b {
			a = append(a, "--no-ff")
		}
		a = append(a, branch)
		return run(ctx, a...)
	})

	reg.Register(model.ToolDefinition{
		Name:        "gh",
		Description: "GitHub CLI integration: create/view issues, PRs, releases. Runs 'gh' commands.",
		InputSchema: obj(map[string]any{
			"command": prop("string", "Full gh command including subcommand and args, e.g. 'pr create --title \"x\" --body \"y\"'"),
		}, "command"),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		cmd := str(args["command"])
		if cmd == "" {
			return "", fmt.Errorf("command required, e.g. 'pr list' or 'issue create --title \"...\"'")
		}
		gh := exec.CommandContext(ctx, "gh", strings.Fields(cmd)...)
		gh.Dir = ws.Root()
		out, err := gh.CombinedOutput()
		s := strings.TrimRight(string(out), "\n")
		if len(s) > maxOutput {
			s = s[:maxOutput] + "\n… (truncated)"
		}
		if err != nil {
			if _, ok := err.(*exec.ExitError); ok {
				return s, nil
			}
			return "", fmt.Errorf("gh not available or failed: %w", err)
		}
		return s, nil
	})
}
