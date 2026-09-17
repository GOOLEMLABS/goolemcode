package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

const dbOutputMax = 20000

func RegisterDB(reg *Registry) {
	reg.Register(model.ToolDefinition{
		Name:        "db_query",
		Description: "Execute a SQL query on SQLite or PostgreSQL. For SQLite uses the .db file directly. For PostgreSQL uses psql.",
		InputSchema: obj(map[string]any{
			"type":            prop("string", "Database type: sqlite, postgres"),
			"database":        prop("string", "Path to .db file (sqlite) or connection string (postgres, e.g. postgres://user:pass@host/db)"),
			"query":           prop("string", "SQL query"),
			"timeout_seconds": prop("integer", "Timeout in seconds (default 30)"),
		}, "type", "database", "query"),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		dbType := str(args["type"])
		database := str(args["database"])
		query := str(args["query"])
		if dbType == "" || database == "" || query == "" {
			return "", fmt.Errorf("type, database, and query are required")
		}

		timeout := 30
		if v, ok := toInt(args["timeout_seconds"]); ok && v > 0 {
			timeout = v
		}

		cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()

		var cmd *exec.Cmd
		switch dbType {
		case "sqlite":
			cmd = exec.CommandContext(cmdCtx, "sqlite3", "-header", "-column", database, query)
		case "postgres":
			cmd = exec.CommandContext(cmdCtx, "psql", "-d", database, "-c", query)
		default:
			return "", fmt.Errorf("unsupported type: %s (use sqlite or postgres)", dbType)
		}

		out, err := cmd.CombinedOutput()
		s := string(out)
		if len(s) > dbOutputMax {
			s = s[:dbOutputMax] + "\n… (truncado)"
		}

		if cmdCtx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("Query aborted: timeout %ds.\n%s", timeout, s), nil
		}

		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			return fmt.Sprintf("Database error: %s\n%s", err, s), nil
		}
		return fmt.Sprintf("[%s] %s\n---\nexit_code: %d\n%s", dbType, database, code, strings.TrimRight(s, "\n")), nil
	})
}
