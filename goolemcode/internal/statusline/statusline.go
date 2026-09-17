package statusline

import (
	"fmt"
	"strings"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

type ModelUsage struct {
	Label        string
	InputTokens  int
	OutputTokens int
	Calls        int
	Cost         float64 // coste USD asociado a ese modelo
}

type Info struct {
	ModelLabel  string
	Dir         string
	Branch      string
	Dirty       bool
	Usage       model.Usage
	Models      []ModelUsage // desglose por modelo (opcional)
	PlanMode    bool
	ScriptRuns  int
	ScriptSaved int
	Savings     string
	CostTotal   string // coste total estimado en USD (ej. "$0.0234")
}

func Render(i Info) string {
	parts := []string{"⬢ " + i.ModelLabel}

	dir := i.Dir
	if i.Branch != "" {
		mark := ""
		if i.Dirty {
			mark = "*"
		}
		dir += fmt.Sprintf(" (%s%s)", i.Branch, mark)
	}
	parts = append(parts, dir)

	// Los tokens siempre se muestran desde cero (0↑ 0↓), no solo cuando >0.
	parts = append(parts, fmt.Sprintf("%s↑ %s↓", Human(i.Usage.InputTokens), Human(i.Usage.OutputTokens)))
	// Desglose por modelo si se usó más de uno en la sesión (p. ej. smart router).
	if len(i.Models) > 1 {
		per := make([]string, 0, len(i.Models))
		for _, m := range i.Models {
			per = append(per, fmt.Sprintf("%s:%s↑/%.4f$", m.Label, Human(m.InputTokens), m.Cost))
		}
		parts = append(parts, "("+strings.Join(per, " ")+")")
	}
	if i.ScriptRuns > 0 {
		parts = append(parts, fmt.Sprintf("📜%d -%s", i.ScriptRuns, Human(i.ScriptSaved)))
	}
	if i.CostTotal == "" {
		parts = append(parts, "coste $0")
	} else {
		parts = append(parts, "coste "+i.CostTotal)
	}
	if i.Savings == "" {
		parts = append(parts, "💰ahorro $0")
	} else {
		parts = append(parts, "💰ahorro "+i.Savings)
	}
	if i.PlanMode {
		parts = append(parts, "plan")
	}
	return "\x1b[2m" + strings.Join(parts, " · ") + "\x1b[0m"
}
func Human(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
