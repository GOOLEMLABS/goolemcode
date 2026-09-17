package tools

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

const (
	grepMaxHits     = 200
	grepMaxFileSize = 5 * 1024 * 1024 // 5 MB: ignora ficheros enormes
	grepSnippetMax  = 200
)

// RegisterGrep registers the `grep` tool: searches for a regex pattern in text
// files under root, skipping binaries and excluded directories.
func RegisterGrep(reg *Registry, ws *Workspace) {
	reg.Register(model.ToolDefinition{
		Name:        "grep",
		Description: "Search for a pattern (regular expression) in the project's text files and returns 'file:line: match'. Excludes node_modules, .git, dist, etc. Use it to locate code instead of blindly reading files.",
		InputSchema: obj(map[string]any{
			"pattern":     prop("string", "Regular expression to search for"),
			"path":        prop("string", "Subdirectory to search in (default '.')"),
			"glob":        prop("string", "File name filter, e.g. '*.go' (optional)"),
			"ignore_case": prop("boolean", "Case-insensitive match (optional)"),
		}, "pattern"),
		Mutating: false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		pattern := str(args["pattern"])
		if strings.TrimSpace(pattern) == "" {
			return "", fmt.Errorf("empty pattern")
		}
		prefix := ""
		if b, ok := args["ignore_case"].(bool); ok && b {
			prefix = "(?i)"
		}
		re, err := regexp.Compile(prefix + pattern)
		if err != nil {
			return "invalid regex: " + err.Error(), nil
		}

		rel := "."
		if s := str(args["path"]); s != "" {
			rel = s
		}
		root := ws.Root()
		base, err := safeJoin(root, rel)
		if err != nil {
			return "", err
		}
		glob := str(args["glob"])

		var hits []string
		truncated := false
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if treeExclude[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if len(hits) >= grepMaxHits {
				truncated = true
				return filepath.SkipAll
			}
			if glob != "" {
				if ok, _ := filepath.Match(glob, d.Name()); !ok {
					return nil
				}
			}
			info, err := d.Info()
			if err != nil || info.Size() > grepMaxFileSize {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			n := len(data)
			if n > 512 {
				n = 512
			}
			if bytes.IndexByte(data[:n], 0) >= 0 {
				return nil // binario
			}
			relp, _ := filepath.Rel(root, p)
			for i, line := range strings.Split(string(data), "\n") {
				if re.MatchString(line) {
					s := strings.TrimSpace(line)
					if len(s) > grepSnippetMax {
						s = s[:grepSnippetMax] + "…"
					}
					hits = append(hits, fmt.Sprintf("%s:%d: %s", relp, i+1, s))
					if len(hits) >= grepMaxHits {
						truncated = true
						return filepath.SkipAll
					}
				}
			}
			return nil
		})

		if len(hits) == 0 {
			return "No matches for: " + pattern, nil
		}
		out := strings.Join(hits, "\n")
		if truncated {
			out += fmt.Sprintf("\n… (truncated to %d matches)", grepMaxHits)
		}
		return out, nil
	})
}
