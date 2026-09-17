package statusline

import (
	"strings"
	"testing"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

func TestRenderFull(t *testing.T) {
	s := Render(Info{
		ModelLabel: "qwen3",
		Dir:        "proyecto",
		Branch:     "main",
		Dirty:      true,
		Usage:      model.Usage{InputTokens: 14000, OutputTokens: 2000},
		PlanMode:   true,
	})
	for _, want := range []string{"⬢ qwen3", "proyecto (main*)", "14.0k↑", "2.0k↓", "plan"} {
		if !strings.Contains(s, want) {
			t.Errorf("falta %q en %q", want, s)
		}
	}
}

func TestRenderMinimal(t *testing.T) {
	s := Render(Info{ModelLabel: "m", Dir: "d"})
	// Sin rama ni plan: solo modelo, dir, tokens desde cero y coste/ahorro $0.
	if strings.Contains(s, "(") || strings.Contains(s, "plan") {
		t.Fatalf("no debía incluir rama/plan: %q", s)
	}
	if !strings.Contains(s, "⬢ m") || !strings.Contains(s, "d") {
		t.Fatalf("debía incluir modelo y dir: %q", s)
	}
	// Los tokens y el coste siempre se muestran desde cero.
	if !strings.Contains(s, "0↑ 0↓") {
		t.Fatalf("debía mostrar tokens 0↑ 0↓: %q", s)
	}
	if !strings.Contains(s, "coste $0") {
		t.Fatalf("debía mostrar coste $0: %q", s)
	}
}

func TestHuman(t *testing.T) {
	cases := map[int]string{500: "500", 1500: "1.5k", 999999: "1000.0k", 2_000_000: "2.0M"}
	for n, want := range cases {
		if got := Human(n); got != want {
			t.Errorf("human(%d) = %q, quería %q", n, got, want)
		}
	}
}

func TestRenderCleanBranchNoStar(t *testing.T) {
	s := Render(Info{ModelLabel: "m", Dir: "d", Branch: "dev", Dirty: false})
	if !strings.Contains(s, "(dev)") || strings.Contains(s, "*") {
		t.Fatalf("rama limpia no debe llevar asterisco: %q", s)
	}
}
