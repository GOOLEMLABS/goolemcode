package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

const (
	sshTimeoutDef = 60
	sshMaxOutput  = 30000
)

func RegisterSSH(reg *Registry) {
	reg.Register(model.ToolDefinition{
		Name:        "ssh_exec",
		Description: "Execute a command on a remote server via SSH. Uses the user's SSH configuration (keys, known_hosts). Requires SSH access to the host.",
		InputSchema: obj(map[string]any{
			"host":            prop("string", "Server hostname or IP"),
			"command":         prop("string", "Command to execute on the remote"),
			"user":            prop("string", "SSH user (optional; defaults to current)"),
			"port":            prop("integer", "SSH port (optional; defaults to 22)"),
			"timeout_seconds": prop("integer", fmt.Sprintf("Timeout in seconds (default %d)", sshTimeoutDef)),
		}, "host", "command"),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		host := str(args["host"])
		command := str(args["command"])
		if host == "" || command == "" {
			return "", fmt.Errorf("host and command are required")
		}

		timeout := sshTimeoutDef
		if v, ok := toInt(args["timeout_seconds"]); ok && v > 0 {
			timeout = v
		}

		sshArgs := []string{"-o", "ConnectTimeout=10", "-o", "BatchMode=yes"}
		if u := str(args["user"]); u != "" {
			sshArgs = append(sshArgs, "-l", u)
		}
		if p, ok := toInt(args["port"]); ok && p > 0 {
			sshArgs = append(sshArgs, "-p", fmt.Sprintf("%d", p))
		}
		sshArgs = append(sshArgs, host, command)

		cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()

		cmd := exec.CommandContext(cmdCtx, "ssh", sshArgs...)
		out, err := cmd.CombinedOutput()
		s := string(out)
		if len(s) > sshMaxOutput {
			s = s[:sshMaxOutput] + "\n… (truncado)"
		}

		label := host
		if u := str(args["user"]); u != "" {
			label = u + "@" + label
		}

		if cmdCtx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("$ ssh %s\n%s\nAborted: exceeded timeout of %ds.", label, s, timeout), nil
		}
		if cmdCtx.Err() == context.Canceled {
			return fmt.Sprintf("$ ssh %s\n%s\nCancelled.", label, s), nil
		}

		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			return fmt.Sprintf("SSH error to %s: %s", label, err.Error()), nil
		}
		return fmt.Sprintf("$ ssh %s\nexit_code: %d\n%s", label, code, strings.TrimRight(s, "\n")), nil
	})
}
