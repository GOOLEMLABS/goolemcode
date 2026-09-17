package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

func RegisterSystemInfo(reg *Registry) {
	reg.Register(model.ToolDefinition{
		Name:        "system_info",
		Description: "System information: disk usage, RAM, processes, CPU load. Read-only.",
		InputSchema: obj(map[string]any{
			"type": prop("string", "Type: disk, memory, processes, load, network, all (default all)"),
		}),
		Mutating: false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		infoType := strings.ToLower(str(args["type"]))
		if infoType == "" {
			infoType = "all"
		}

		var parts []string

		switch infoType {
		case "disk", "all":
			if out, err := exec.Command("df", "-h").Output(); err == nil {
				parts = append(parts, "=== DISK ===\n"+string(out))
			} else {
				parts = append(parts, "=== DISK ===\n(not available)")
			}
		}

		switch infoType {
		case "memory", "all":
			if out, err := exec.Command("free", "-h").Output(); err == nil {
				parts = append(parts, "=== MEMORY ===\n"+string(out))
			} else if out, err := exec.Command("vm_stat").Output(); err == nil {
				parts = append(parts, "=== MEMORY ===\n"+string(out))
			} else {
				out, _ := exec.Command("cat", "/proc/meminfo").Output()
				if len(out) > 0 {
					parts = append(parts, "=== MEMORY ===\n"+string(out))
				} else {
					parts = append(parts, "=== MEMORY ===\n(not available)")
				}
			}
		}

		switch infoType {
		case "load", "all":
			if out, err := exec.Command("uptime").Output(); err == nil {
				parts = append(parts, "=== LOAD ===\n"+string(out))
			}
		}

		switch infoType {
		case "network", "all":
			if out, err := exec.Command("ip", "addr", "show").Output(); err == nil {
				lines := strings.Split(string(out), "\n")
				show := lines
				if len(lines) > 30 {
					show = lines[:30]
				}
				parts = append(parts, "=== NETWORK ===\n"+strings.Join(show, "\n"))
				if len(lines) > 30 {
					parts = append(parts, "… (showing 30 of "+fmt.Sprintf("%d", len(lines))+" lines)")
				}
			} else if out, err := exec.Command("ifconfig").Output(); err == nil {
				parts = append(parts, "=== NETWORK ===\n"+string(out))
			}
		}

		switch infoType {
		case "processes", "all":
			if infoType == "processes" {
				if out, err := exec.Command("ps", "aux", "--sort=-%mem").Output(); err == nil {
					lines := strings.Split(string(out), "\n")
					show := lines
					if len(lines) > 20 {
						show = lines[:20]
					}
					parts = append(parts, "=== PROCESSES (top 20 by memory) ===\n"+strings.Join(show, "\n"))
				}
			}
		}

		if len(parts) == 0 {
			return fmt.Sprintf("Unknown type: %s (use disk, memory, processes, load, network, all)", infoType), nil
		}
		return strings.Join(parts, "\n\n"), nil
	})
}
