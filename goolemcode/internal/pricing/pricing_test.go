package pricing

import (
	"math"
	"testing"
)

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}

func TestRateCost(t *testing.T) {
	r := Rate{Input: 1.0, Output: 5.0}
	c := r.Cost(1000, 500)
	// 1000/1M * $1 + 500/1M * $5 = $0.001 + $0.0025 = $0.0035
	if round2(c) != round2(0.0035) {
		t.Errorf("expected $0.0035, got $%.6f", c)
	}
}

func TestRateCostZero(t *testing.T) {
	r := Rate{Input: 0, Output: 0}
	c := r.Cost(1000000, 1000000)
	if c != 0 {
		t.Errorf("expected 0, got %f", c)
	}
}

func TestRateString(t *testing.T) {
	r := Rate{Input: 5.00, Output: 25.00}
	s := r.String()
	if s != "$5.0000/M↑ $25.0000/M↓" {
		t.Errorf("unexpected string: %s", s)
	}
}

func TestModelRateMapDefaultRates(t *testing.T) {
	m := NewModelRateMap()
	if m == nil {
		t.Fatal("NewModelRateMap returned nil")
	}
	// Debe tener al menos los modelos conocidos
	expected := []string{
		"Claude (claude-opus-4-8)",
		"DeepSeek (deepseek-v4-pro)",
		"Ollama (gemma4:31b)",
		"Ollama (qwen3:8b)",
	}
	for _, label := range expected {
		if _, ok := m.Get(label); !ok {
			t.Errorf("expected rate for %q", label)
		}
	}
}

func TestModelRateMapGetCaseInsensitive(t *testing.T) {
	m := NewModelRateMap()
	// Búsqueda por subcadena
	r, ok := m.Get("sonnet")
	if !ok {
		t.Fatal("expected to find sonnet by substring")
	}
	if r.Input != 2.00 || r.Output != 10.00 {
		t.Errorf("unexpected sonnet rate: %v", r)
	}
}

func TestModelRateMapGetNotFound(t *testing.T) {
	m := NewModelRateMap()
	_, ok := m.Get("nonexistent-model-v99")
	if ok {
		t.Error("expected false for unknown model")
	}
}

func TestModelRateMapSet(t *testing.T) {
	m := NewModelRateMap()
	m.Set("custom-model", Rate{Input: 0.5, Output: 1.5})
	r, ok := m.Get("custom-model")
	if !ok {
		t.Fatal("expected to find custom model")
	}
	if r.Input != 0.5 || r.Output != 1.5 {
		t.Errorf("unexpected rate: %v", r)
	}
}

func TestModelRateMapCost(t *testing.T) {
	m := NewModelRateMap()
	c, ok := m.Cost("DeepSeek (deepseek-v4-pro)", 100000, 50000)
	if !ok {
		t.Fatal("expected to find deepseek-v4-pro")
	}
	// esperado: 0.1M * $0.435 + 0.05M * $0.87 = $0.0435 + $0.0435 = $0.087
	if round2(c) != round2(0.087) {
		t.Errorf("expected $0.087, got $%.6f", c)
	}
}

func TestSavingsReportBasic(t *testing.T) {
	m := NewModelRateMap()
	usage := map[string]UsageRow{
		"Claude (claude-opus-4-8)": {InputTokens: 100000, OutputTokens: 50000},
	}
	savings, actual, hypo, report := m.SavingsReport(usage, "Claude (claude-opus-4-8)")
	if hypo < actual {
		t.Errorf("hypothetical cost %.6f should be >= actual %.6f", hypo, actual)
	}
	if savings != 0 {
		t.Errorf("savings should be 0 for single model, got %.6f", savings)
	}
	if report == "" {
		t.Error("expected non-empty report")
	}
}

func TestSavingsReportWithSmartRouting(t *testing.T) {
	m := NewModelRateMap()
	usage := map[string]UsageRow{
		"Claude (claude-opus-4-8)": {InputTokens: 50000, OutputTokens: 25000},
		"Ollama (qwen3:14b)":       {InputTokens: 200000, OutputTokens: 100000},
	}
	savings, actual, hypo, report := m.SavingsReport(usage, "Claude (claude-opus-4-8)")
	if hypo <= actual {
		t.Errorf("hypothetical %.6f should be > actual %.6f", hypo, actual)
	}
	if savings <= 0 {
		t.Errorf("expected positive savings, got %.6f", savings)
	}
	if report == "" {
		t.Error("expected non-empty report")
	}
	t.Logf("Savings: $%.4f, Actual: $%.4f, Hypothetical: $%.4f", savings, actual, hypo)
}

func TestSavingsReportWithAllPrimary(t *testing.T) {
	m := NewModelRateMap()
	usage := map[string]UsageRow{
		"Claude (claude-opus-4-8)": {InputTokens: 100000, OutputTokens: 50000},
		"Ollama (gemma4:31b)":      {InputTokens: 0, OutputTokens: 0},
	}
	savings, _, _, _ := m.SavingsReport(usage, "Claude (claude-opus-4-8)")
	if savings != 0 {
		t.Errorf("expected 0 savings when secondary used 0 tokens, got %.6f", savings)
	}
}

func TestSavingsReportUnknownPrimary(t *testing.T) {
	m := NewModelRateMap()
	usage := map[string]UsageRow{
		"unknown-model": {InputTokens: 1000, OutputTokens: 500},
	}
	savings, actual, _, _ := m.SavingsReport(usage, "unknown-model")
	if savings != 0 {
		t.Errorf("expected 0 savings for unknown model, got %.6f", savings)
	}
	if actual == 0 {
		t.Error("actual cost should be > 0 even with unknown model")
	}
}

func TestSavingsReportNoData(t *testing.T) {
	m := NewModelRateMap()
	savings, actual, _, report := m.SavingsReport(nil, "Claude (claude-opus-4-8)")
	if savings != 0 || actual != 0 {
		t.Errorf("expected 0 savings and actual for no data, got savings=%.6f actual=%.6f", savings, actual)
	}
	if report == "" {
		t.Error("expected non-empty report even with no data")
	}
}

func TestShortLabel(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"Claude (claude-opus-4-8)", "claude-opus-4-8"},
		{"Ollama (gemma4:31b)", "gemma4:31b"},
		{"DeepSeek (deepseek-v4-pro)", "deepseek-v4-pro"},
		{"NoParens", "NoParens"},
	}
	for _, tt := range tests {
		got := shortLabel(tt.input)
		if got != tt.expected {
			t.Errorf("shortLabel(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestSavingsPercent(t *testing.T) {
	tests := []struct {
		hypo, savings, expected float64
	}{
		{100, 30, 30.0},
		{100, 100, 100.0},
		{0, 10, 0},
		{50, 0, 0},
	}
	for _, tt := range tests {
		got := savingsPercent(tt.hypo, tt.savings)
		if got != tt.expected {
			t.Errorf("savingsPercent(%.2f, %.2f) = %.1f, want %.1f", tt.hypo, tt.savings, got, tt.expected)
		}
	}
}
