package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/proc"
)

const dockerOutputMax = 30000

func RegisterDocker(reg *Registry) {
	reg.Register(model.ToolDefinition{
		Name:        "docker_exec",
		Description: "Execute a command inside a Docker container. Useful for managing services, inspecting internal processes, etc.",
		InputSchema: obj(map[string]any{
			"container":       prop("string", "Container name or ID"),
			"command":         prop("string", "Command to execute inside the container (e.g. ls -la /app)"),
			"timeout_seconds": prop("integer", "Timeout in seconds (default 30)"),
		}, "container", "command"),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		container := str(args["container"])
		command := str(args["command"])
		if container == "" || command == "" {
			return "", fmt.Errorf("container and command are required")
		}

		timeout := 30
		if v, ok := toInt(args["timeout_seconds"]); ok && v > 0 {
			timeout = v
		}

		cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()

		cmd := proc.Command(cmdCtx, "docker", "exec", container, "sh", "-c", command)
		out, err := cmd.CombinedOutput()
		s := string(out)
		if len(s) > dockerOutputMax {
			s = s[:dockerOutputMax] + "\n… (truncado)"
		}

		if cmdCtx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("$ docker exec %s sh -c %q\n%s\nAborted: timeout %ds.", container, command, s, timeout), nil
		}

		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			return fmt.Sprintf("Docker exec error: %s", err), nil
		}
		return fmt.Sprintf("$ docker exec %s sh -c %q\nexit_code: %d\n%s", container, command, code, strings.TrimRight(s, "\n")), nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "docker_logs",
		Description: "Show recent logs of a Docker container.",
		InputSchema: obj(map[string]any{
			"container": prop("string", "Container name or ID"),
			"lines":     prop("integer", "Number of lines to show (default 50)"),
			"follow":    prop("boolean", "Follow logs in real-time (optional, incompatible with lines)"),
		}, "container"),
		Mutating: false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		container := str(args["container"])
		if container == "" {
			return "", fmt.Errorf("container is required")
		}

		dockerArgs := []string{"logs"}
		if follow, ok := args["follow"].(bool); ok && follow {
			dockerArgs = append(dockerArgs, "--follow", "--tail", "50")
		} else {
			lines := 50
			if v, ok := toInt(args["lines"]); ok && v > 0 {
				lines = v
			}
			dockerArgs = append(dockerArgs, "--tail", fmt.Sprintf("%d", lines))
		}
		dockerArgs = append(dockerArgs, container)

		cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		cmd := proc.Command(cmdCtx, "docker", dockerArgs...)
		out, err := cmd.CombinedOutput()
		s := string(out)
		if len(s) > dockerOutputMax {
			s = s[:dockerOutputMax] + "\n… (truncado)"
		}
		if err != nil {
			return fmt.Sprintf("Docker logs error: %s\n%s", err, s), nil
		}
		return fmt.Sprintf("$ docker logs --tail %d %s\n%s", 50, container, strings.TrimRight(s, "\n")), nil
	})
}
