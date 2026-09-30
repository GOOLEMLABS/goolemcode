package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/console"
	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/proc"
)

const (
	maxOutput     = 30000
	maxAutoLines  = 400
	treeMaxEntry  = 800
	treeMaxDepth  = 6
	cmdTimeoutDef = 180 // segundos por defecto para execute_command
)

var treeExclude = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true, ".next": true,
	"out": true, "coverage": true, ".venv": true, "venv": true, "__pycache__": true,
	".pytest_cache": true, ".idea": true, ".gradle": true,
}

// RegisterLocal registers system tools (sandbox to mobile ws root).
func RegisterLocal(reg *Registry, ws *Workspace) {
	safe := func(rel string) (string, error) { return safeJoin(ws.Root(), rel) }

	reg.Register(model.ToolDefinition{
		Name:        "read_file",
		Description: "Read a text file. Use line_start/line_end (1-indexed) for a range to save tokens.",
		InputSchema: obj(map[string]any{
			"path":       prop("string", "Path (relative to the working directory, or absolute for another directory)"),
			"line_start": prop("integer", "First line (optional)"),
			"line_end":   prop("integer", "Last line (optional)"),
		}, "path"),
		Mutating: false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		p, err := safe(str(args["path"]))
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "Does not exist or cannot be read: " + str(args["path"]), nil
		}
		lines := strings.Split(string(data), "\n")
		total := len(lines)
		explicit := args["line_start"] != nil || args["line_end"] != nil
		start := 1
		if v, ok := toInt(args["line_start"]); ok && v > 1 {
			start = v
		}
		end := total
		if v, ok := toInt(args["line_end"]); ok && v < end {
			end = v
		} else if !explicit && total > maxAutoLines {
			end = maxAutoLines
		}
		if start > total {
			start = total
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s (lines %d-%d of %d)\n", str(args["path"]), start, end, total)
		for i := start - 1; i < end && i < total; i++ {
			fmt.Fprintf(&sb, "%d\t%s\n", i+1, lines[i])
		}
		if !explicit && total > maxAutoLines {
			fmt.Fprintf(&sb, "… (%d more lines; request a range with line_start/line_end)", total-maxAutoLines)
		}
		return sb.String(), nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "write_file",
		Description: "Create or overwrite a file atomically.",
		InputSchema: obj(map[string]any{
			"path":    prop("string", "Path (relative to the working directory, or absolute for another directory)"),
			"content": prop("string", "Full content"),
		}, "path", "content"),
		Mutating: true,
	}, func(_ context.Context, args map[string]any) (string, error) {
		p, err := safe(str(args["path"]))
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		content := str(args["content"])
		tmp := fmt.Sprintf("%s.%d.tmp", p, os.Getpid())
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, p); err != nil {
			return "", err
		}
		return fmt.Sprintf("Written %s (%d bytes)", str(args["path"]), len(content)), nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "edit_file",
		Description: "Replace old_string with new_string. By default requires a single match; use replace_all:true to replace ALL occurrences (useful for renaming a symbol).",
		InputSchema: obj(map[string]any{
			"path":        prop("string", "Path (relative to the working directory, or absolute for another directory)"),
			"old_string":  prop("string", "Text to replace (MUST be unique)"),
			"new_string":  prop("string", "Replacement text"),
			"replace_all": prop("boolean", "Replace all occurrences (default false)"),
		}, "path", "old_string", "new_string"),
		Mutating: true,
	}, func(_ context.Context, args map[string]any) (string, error) {
		p, err := safe(str(args["path"]))
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "Does not exist: " + str(args["path"]), nil
		}
		text := string(data)
		old := str(args["old_string"])
		if old == "" {
			return "old_string is empty.", nil
		}
		count := strings.Count(text, old)
		replaceAll, _ := args["replace_all"].(bool)
		switch {
		case count == 0:
			return "old_string not found in file.", nil
		case replaceAll:
			text = strings.ReplaceAll(text, old, str(args["new_string"]))
			if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Edited %s (%d occurrences)", str(args["path"]), count), nil
		case count == 1:
			text = strings.Replace(text, old, str(args["new_string"]), 1)
			if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
				return "", err
			}
			return "Edited " + str(args["path"]), nil
		default:
			return fmt.Sprintf("old_string appears %d times; make it unique or use replace_all:true.", count), nil
		}
	})

	reg.Register(model.ToolDefinition{
		Name:        "view_directory_tree",
		Description: "Show the project directory tree, excluding node_modules, .git, etc.",
		InputSchema: obj(map[string]any{
			"path":      prop("string", "Subdirectory (defaults to project root)"),
			"max_depth": prop("integer", fmt.Sprintf("Maximum depth (default %d)", treeMaxDepth)),
		}),
		Mutating: false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		rel := "."
		if s := str(args["path"]); s != "" {
			rel = s
		}
		base, err := safe(rel)
		if err != nil {
			return "", err
		}
		maxDepth := treeMaxDepth
		if v, ok := toInt(args["max_depth"]); ok {
			maxDepth = v
		}
		var sb strings.Builder
		count := 0
		var walk func(dir string, depth int, prefix string)
		walk = func(dir string, depth int, prefix string) {
			if depth > maxDepth || count >= treeMaxEntry {
				return
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			sort.Slice(entries, func(i, j int) bool {
				if entries[i].IsDir() != entries[j].IsDir() {
					return entries[i].IsDir()
				}
				return entries[i].Name() < entries[j].Name()
			})
			for _, e := range entries {
				if treeExclude[e.Name()] {
					continue
				}
				if count >= treeMaxEntry {
					sb.WriteString(prefix + "… (truncado)\n")
					return
				}
				count++
				if e.IsDir() {
					sb.WriteString(prefix + e.Name() + "/\n")
					walk(filepath.Join(dir, e.Name()), depth+1, prefix+"  ")
				} else {
					sb.WriteString(prefix + e.Name() + "\n")
				}
			}
		}
		rl, _ := filepath.Rel(ws.Root(), base)
		if rl == "" {
			rl = "."
		}
		sb.WriteString(rl + "\n")
		walk(base, 1, "  ")
		return strings.TrimRight(sb.String(), "\n"), nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "list_dir",
		Description: "List directory contents (file/directory names). Does not use a tree view.",
		InputSchema: obj(map[string]any{"path": prop("string", "Path (relative or absolute)")}),
		Mutating:    false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		rel := "."
		if s := str(args["path"]); s != "" {
			rel = s
		}
		p, err := safe(rel)
		if err != nil {
			return "", err
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return "Not a directory: " + rel, nil
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name()+"/")
			} else {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		return strings.Join(names, "\n"), nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "execute_command",
		Description: fmt.Sprintf("Execute a shell command (tests, lint, build…). Returns stdout+stderr and exit code. Default timeout %ds (adjust with timeout_seconds for long builds).", cmdTimeoutDef),
		InputSchema: obj(map[string]any{
			"command":         prop("string", "Full command"),
			"timeout_seconds": prop("integer", fmt.Sprintf("Seconds before timeout (default %d)", cmdTimeoutDef)),
		}, "command"),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		timeout := cmdTimeoutDef
		if v, ok := toInt(args["timeout_seconds"]); ok && v > 0 {
			timeout = v
		}
		cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
		cmd := proc.Command(cmdCtx, "sh", "-c", str(args["command"]))
		cmd.Dir = ws.Root()
		// Salida en vivo (se ve el progreso mientras corre) + captura acotada.
		cap := console.NewCapture(maxOutput)
		cmd.Stdout = cap
		cmd.Stderr = cap
		err := cmd.Run()
		s := cap.String()
		if cmdCtx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("$ %s\nAborted: exceeded timeout of %ds.\n%s", str(args["command"]), timeout, s), nil
		}
		if cmdCtx.Err() == context.Canceled {
			return fmt.Sprintf("$ %s\nCancelled by user.\n%s", str(args["command"]), s), nil
		}
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			return "Could not execute: " + err.Error(), nil
		}
		return fmt.Sprintf("$ %s\nexit_code: %d\n%s", str(args["command"]), code, s), nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "batch_edit",
		Description: "Search and replace text across MULTIPLE files. Confirms each file. Mutating: creates checkpoints.",
		InputSchema: obj(map[string]any{
			"pattern":     prop("string", "Text or regex to search for"),
			"replacement": prop("string", "Replacement text"),
			"glob":        prop("string", "File glob pattern (e.g. '*.go', 'src/**/*.ts'). Default: all files"),
			"is_regex":    prop("boolean", "Treat pattern as regex (default false, plain string)"),
		}, "pattern", "replacement"),
		Mutating: true,
	}, func(_ context.Context, args map[string]any) (string, error) {
		pattern := str(args["pattern"])
		replacement := str(args["replacement"])
		glob := str(args["glob"])
		isRegex := false
		if b, ok := args["is_regex"].(bool); ok && b {
			isRegex = true
		}
		root := ws.Root()
		searchDir := root
		if glob != "" {
			matches, err := filepath.Glob(filepath.Join(root, glob))
			if err != nil || len(matches) == 0 {
				return "", fmt.Errorf("no files match glob: %s", glob)
			}
			var results []string
			for _, p := range matches {
				data, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				rel, _ := filepath.Rel(root, p)
				old := string(data)
				var neu string
				if isRegex {
					re, err := regexp.Compile(pattern)
					if err != nil {
						return "", fmt.Errorf("invalid regex: %w", err)
					}
					neu = re.ReplaceAllString(old, replacement)
				} else {
					neu = strings.ReplaceAll(old, pattern, replacement)
				}
				if neu != old {
					if err := os.WriteFile(p, []byte(neu), 0o644); err != nil {
						results = append(results, fmt.Sprintf("✗ %s: write error: %s", rel, err))
					} else {
						results = append(results, fmt.Sprintf("✓ %s: replaced", rel))
					}
				}
			}
			if len(results) == 0 {
				return "No files matched or no replacements made.", nil
			}
			return strings.Join(results, "\n"), nil
		}
		// No glob: walk entire workspace
		var results []string
		_ = filepath.WalkDir(searchDir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				if d != nil && d.IsDir() && treeExclude[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if isBinary(p) {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			old := string(data)
			var neu string
			if isRegex {
				re, err := regexp.Compile(pattern)
				if err != nil {
					return nil
				}
				neu = re.ReplaceAllString(old, replacement)
			} else {
				neu = strings.ReplaceAll(old, pattern, replacement)
			}
			if neu != old {
				rel, _ := filepath.Rel(root, p)
				if err := os.WriteFile(p, []byte(neu), 0o644); err != nil {
					results = append(results, fmt.Sprintf("✗ %s: write error: %s", rel, err))
				} else {
					results = append(results, fmt.Sprintf("✓ %s: replaced", rel))
				}
			}
			return nil
		})
		if len(results) == 0 {
			return "No replacements made.", nil
		}
		return strings.Join(results, "\n"), nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "edit_lines",
		Description: "Replace specific line range in a file with new content. 1-indexed, inclusive. Safer than edit_file for large files.",
		InputSchema: obj(map[string]any{
			"path":        prop("string", "File path (relative or absolute)"),
			"line_start":  prop("integer", "First line to replace (1-indexed)"),
			"line_end":    prop("integer", "Last line to replace (inclusive)"),
			"new_content": prop("string", "New content for those lines"),
		}, "path", "line_start", "line_end", "new_content"),
		Mutating: true,
	}, func(_ context.Context, args map[string]any) (string, error) {
		p, err := safeJoin(ws.Root(), str(args["path"]))
		if err != nil {
			return "", err
		}
		start, ok1 := toInt(args["line_start"])
		end, ok2 := toInt(args["line_end"])
		if !ok1 || !ok2 || start < 1 || end < start {
			return "", fmt.Errorf("line_start and line_end must be positive integers, start <= end")
		}
		content := str(args["new_content"])

		data, err := os.ReadFile(p)
		if err != nil {
			return "does not exist or cannot be read: " + str(args["path"]), nil
		}
		lines := strings.Split(string(data), "\n")
		total := len(lines)
		if start > total {
			return "", fmt.Errorf("line_start %d > file has %d lines", start, total)
		}
		if end > total {
			end = total
		}

		var sb strings.Builder
		for i, line := range lines {
			if i+1 >= start && i+1 <= end {
				if i+1 == start {
					sb.WriteString(content)
					if !strings.HasSuffix(content, "\n") {
						sb.WriteString("\n")
					}
				}
			} else {
				sb.WriteString(line)
				if i < total-1 {
					sb.WriteString("\n")
				}
			}
		}

		tmp := fmt.Sprintf("%s.%d.tmp", p, os.Getpid())
		if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, p); err != nil {
			return "", err
		}
		return fmt.Sprintf("Replaced lines %d-%d in %s (%d lines total)", start, end, str(args["path"]), total), nil
	})
}

// safeJoin resolves a path for file tools. An ABSOLUTE path is accepted as-is
// (to manage other directories, like execute_command does with bash). A RELATIVE
// path is resolved within the working directory and cannot escape with "../"
// (guardrail for relative paths; to leave the tree use an absolute path on
// purpose). Actual mutation safety comes from the permission gate + checkpoints,
// not this function.
func safeJoin(root, rel string) (string, error) {
	p := rel
	if !filepath.IsAbs(rel) {
		p = filepath.Join(root, rel)
	}
	p = filepath.Clean(p)
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path outside working directory: %s (must be within %s)", rel, root)
	}
	return p, nil
}

// --- schema/args helpers ---

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

func prop(typ, desc string) map[string]any {
	m := map[string]any{"type": typ}
	if desc != "" {
		m["description"] = desc
	}
	return m
}

func obj(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{} // evita "properties":null que DeepSeek rechaza
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func isBinary(p string) bool {
	data, err := os.ReadFile(p)
	if err != nil || len(data) == 0 {
		return false
	}
	n := len(data)
	if n > 512 {
		n = 512
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}
