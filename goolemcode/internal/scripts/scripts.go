package scripts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/console"
	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/proc"
	"github.com/GOOLEMLABS/goolemcode/internal/securefs"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
)

type Script struct {
	Name        string
	Path        string
	Description string
	Args        string
}

type Stats struct {
	Runs   int
	Tokens int // tokens ahorrados estimados
}

type Store struct {
	dir   string
	stats Stats
}

func New(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Dir() string  { return s.dir }
func (s *Store) Stats() Stats { return s.stats }

func (s *Store) List() []Script {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var scripts []Script
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
			continue
		}
		sc := Script{Name: strings.TrimSuffix(e.Name(), ".py"), Path: filepath.Join(s.dir, e.Name())}
		data, err := os.ReadFile(sc.Path)
		if err != nil {
			continue
		}
		desc, args := parseDocstring(string(data))
		sc.Description = desc
		sc.Args = args
		scripts = append(scripts, sc)
	}
	sort.Slice(scripts, func(i, j int) bool { return scripts[i].Name < scripts[j].Name })
	return scripts
}

func (s *Store) Get(name string) *Script {
	for _, sc := range s.List() {
		if sc.Name == name {
			return &sc
		}
	}
	return nil
}

func (s *Store) Run(ctx context.Context, name string, scriptArgs string, timeout int) (string, int, error) {
	sc := s.Get(name)
	if sc == nil {
		return "", -1, fmt.Errorf("script not found: %s", name)
	}
	if timeout <= 0 {
		timeout = 60
	}

	// Estimate saved tokens: what the script would take if the agent generated it
	if data, err := os.ReadFile(sc.Path); err == nil {
		s.stats.Tokens += len(string(data)) / 4
	}
	s.stats.Runs++
	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := proc.Command(cmdCtx, "python3", sc.Path)
	if scriptArgs != "" {
		cmd.Args = append(cmd.Args, strings.Fields(scriptArgs)...)
	}
	cap := console.NewCapture(20000) // salida en vivo + captura acotada
	cmd.Stdout = cap
	cmd.Stderr = cap

	err := cmd.Run()
	sout := cap.String()

	if cmdCtx.Err() == context.DeadlineExceeded {
		return sout + fmt.Sprintf("\nAborted: timeout %ds.", timeout), -1, nil
	}

	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return "", -1, err
	}
	return sout, code, nil
}

func (s *Store) Index() string {
	scripts := s.List()
	if len(scripts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("AVAILABLE SCRIPTS (use script_run to execute, script_create to create a new one):\n")
	for _, sc := range scripts {
		b.WriteString(fmt.Sprintf("  - %s", sc.Name))
		if sc.Description != "" {
			b.WriteString(": " + sc.Description)
		}
		if sc.Args != "" {
			b.WriteString(fmt.Sprintf(" (args: %s)", sc.Args))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func parseDocstring(code string) (description, args string) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, `"""`) && !strings.HasPrefix(code, "'''") {
		return "", ""
	}
	quote := code[:3]
	rest := code[3:]
	idx := strings.Index(rest, quote)
	if idx < 0 {
		return "", ""
	}
	doc := rest[:idx]
	lines := strings.Split(doc, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return "", ""
	}
	description = strings.TrimSpace(lines[0])
	for i := 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(strings.ToLower(t), "args:") {
			args = strings.TrimSpace(t[5:])
			break
		}
		if strings.HasPrefix(strings.ToLower(t), "arguments:") || strings.HasPrefix(strings.ToLower(t), "argumentos:") {
			args = strings.TrimSpace(t[11:])
			break
		}
	}
	return description, args
}

func RegisterTools(reg *tools.Registry, store *Store) {
	reg.Register(model.ToolDefinition{
		Name:        "script_list",
		Description: "List available scripts in the local library (~/.goolem/scripts/). Each script is a .py that performs a common task faster than generating from scratch.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Mutating:    false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		scripts := store.List()
		if len(scripts) == 0 {
			return "No scripts in the library yet. Create one with script_create.", nil
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Available scripts (%s):\n\n", store.Dir()))
		for _, sc := range scripts {
			b.WriteString(fmt.Sprintf("  %s.py", sc.Name))
			if sc.Description != "" {
				b.WriteString(fmt.Sprintf("\n    %s", sc.Description))
			}
			if sc.Args != "" {
				b.WriteString(fmt.Sprintf("\n    args: %s", sc.Args))
			}
			b.WriteString("\n")
		}
		b.WriteString("\nUse script_run <name> [args] to execute.")
		return b.String(), nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "script_run",
		Description: "Execute a script from the local library. Faster than generating code from scratch. Use script_list to see available scripts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "Script name (without .py)"},
				"args":    map[string]any{"type": "string", "description": "Arguments for the script (optional, space-separated)"},
				"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (optional, default 60)"},
			},
			"required": []string{"name"},
		},
		Mutating: false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		name, _ := args["name"].(string)
		if name == "" {
			return "", fmt.Errorf("name is required")
		}
		scriptArgs, _ := args["args"].(string)
		timeout := 60
		if v, ok := toInt(args["timeout"]); ok && v > 0 {
			timeout = v
		}
		out, code, err := store.Run(ctx, name, scriptArgs, timeout)
		if err != nil {
			return "", err
		}
		prefix := fmt.Sprintf("$ python3 %s.py %s\n", name, scriptArgs)
		if code != 0 {
			return prefix + out + fmt.Sprintf("\nexit_code: %d", code), nil
		}
		return prefix + out, nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "script_create",
		Description: "Create a new Python script in the local library (~/.goolem/scripts/). The script must have a docstring with description and args. It will be run with python3 <script>.py [args]. Use it when you find yourself repeating the same operation multiple times.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Script name (without .py)"},
				"code":        map[string]any{"type": "string", "description": "Full Python code. Must start with docstring \"\"\"Description.\nArgs: <space-separated arguments>\"\"\""},
				"description": map[string]any{"type": "string", "description": "Brief description (injected into system context so the agent knows about it)"},
			},
			"required": []string{"name", "code"},
		},
		Mutating: true,
	}, func(_ context.Context, args map[string]any) (string, error) {
		name, _ := args["name"].(string)
		code, _ := args["code"].(string)
		if name == "" || code == "" {
			return "", fmt.Errorf("name and code are required")
		}
		path := filepath.Join(store.Dir(), name+".py")
		if err := securefs.WriteFile(path, []byte(code)); err != nil {
			return "", err
		}
		desc, _ := args["description"].(string)
		msg := fmt.Sprintf("Script created: %s.py", name)
		if desc != "" {
			msg += fmt.Sprintf(" (%s)", desc)
		}
		return msg, nil
	})
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
