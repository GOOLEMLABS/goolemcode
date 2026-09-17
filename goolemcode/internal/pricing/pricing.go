// Package pricing ofrece tabla de precios de LLMs y funciones de coste para
// calcular gastos estimados en USD por consumo de tokens, con soporte para
// Smart Router (ahorro por usar modelos más baratos en tareas simples).
package pricing

import (
	"fmt"
	"math"
	"strings"
	"sync"
)

// Rate describe el coste por millón de tokens (USD).
type Rate struct {
	Input  float64 // $/M tokens de entrada
	Output float64 // $/M tokens de salida
}

// Cost calcula el coste en USD de un consumo de tokens.
func (r Rate) Cost(inputTokens, outputTokens int) float64 {
	return float64(inputTokens)/1_000_000*r.Input + float64(outputTokens)/1_000_000*r.Output
}

// String muestra una descripción legible de la tarifa.
func (r Rate) String() string {
	return fmt.Sprintf("$%.4f/M↑ $%.4f/M↓", r.Input, r.Output)
}

// ModelRateMap es un mapa de etiquetas de modelo (Label()) a su tarifa.
type ModelRateMap struct {
	mu    sync.RWMutex
	rates map[string]Rate
}

// NewModelRateMap crea un mapa con las tarifas por defecto de todos los modelos
// conocidos. Los precios corresponden a julio 2026.
func NewModelRateMap() *ModelRateMap {
	return &ModelRateMap{
		rates: defaultRates(),
	}
}

// Get devuelve la tarifa de un modelo. ok=false si no se encuentra.
// La búsqueda es case-insensitive y acepta subcadenas (p. ej. "Claude" busca
// cualquier claude, "sonnet" busca el primer modelo que contenga "sonnet").
func (m *ModelRateMap) Get(label string) (Rate, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if r, ok := m.rates[label]; ok {
		return r, ok
	}
	// Búsqueda flexible: subcadena case-insensitive
	lower := strings.ToLower(label)
	for key, r := range m.rates {
		if strings.Contains(lower, strings.ToLower(key)) {
			return r, true
		}
		if strings.Contains(strings.ToLower(key), lower) {
			return r, true
		}
	}
	return Rate{}, false
}

// Set registra (o sobreescribe) la tarifa de un modelo.
func (m *ModelRateMap) Set(label string, r Rate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rates[label] = r
}

// Rates devuelve una copia del mapa completo.
func (m *ModelRateMap) Rates() map[string]Rate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]Rate, len(m.rates))
	for k, v := range m.rates {
		out[k] = v
	}
	return out
}

// Cost calcula el coste de un consumo de tokens para un modelo dado.
func (m *ModelRateMap) Cost(label string, inputTokens, outputTokens int) (float64, bool) {
	r, ok := m.Get(label)
	if !ok {
		return 0, false
	}
	return r.Cost(inputTokens, outputTokens), true
}

// SavingsReport calcula el ahorro entre usar exclusivamente el modelo más caro
// frente al uso real (con smart routing), dado un desglose de consumo por modelo.
// Devuelve el ahorro en USD, el coste real, el coste hipotético del caro, y un
// texto descriptivo.
func (m *ModelRateMap) SavingsReport(usageByModel map[string]UsageRow, primaryLabel string) (savingsUSD, actualCost, hypotheticalCost float64, report string) {
	return m.savingsReport(usageByModel, primaryLabel)
}

// SavingsReportFull es como SavingsReport pero devuelve 5 valores: ahorro, coste real,
// coste hipotético, texto descriptivo, y un booleano indicando si se encontró la tarifa
// del modelo primario. primaryCalls/secondaryCalls son el número de peticiones enviadas
// a cada modelo (para el detalle "X al primario vs Y al secundario"); secondaryLabel es
// la etiqueta del modelo secundario.
func (m *ModelRateMap) SavingsReportFull(usageByModel map[string]UsageRow, primaryLabel string, primaryCalls, secondaryCalls int, secondaryLabel string) (savingsUSD, actualCost, hypotheticalCost float64, report string, primaryFound bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var totalInput, totalOutput int
	var totalCalls int
	var accruingCost float64
	var lines []string

	// Encontrar la tarifa del modelo primario (el caro)
	primaryRate, primaryFound := m.findRate(primaryLabel)

	for label, row := range usageByModel {
		r, found := m.findRate(label)
		if !found {
			r = Rate{Input: 0.002, Output: 0.010} // estimación conservadora si no se conoce
		}
		cost := r.Cost(row.InputTokens, row.OutputTokens)
		accruingCost += cost
		totalInput += row.InputTokens
		totalOutput += row.OutputTokens
		totalCalls += row.Calls

		lines = append(lines, fmt.Sprintf("  %-30s (%3d llamadas) %6d↑ %6d↓ → $%.4f",
			label, row.Calls, row.InputTokens, row.OutputTokens, cost))
	}

	// Coste hipotético: si todo lo hubiera hecho el modelo primario
	if primaryFound {
		hypotheticalCost = primaryRate.Cost(totalInput, totalOutput)
	} else {
		hypotheticalCost = accruingCost // no podemos calcular
	}

	savingsUSD = hypotheticalCost - accruingCost
	if savingsUSD < 0 {
		savingsUSD = 0 // no puede ser negativo
	}

	var b strings.Builder
	b.WriteString("📊 Smart Router — consumo y ahorro:\n")
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if primaryFound {
		b.WriteString(fmt.Sprintf("  %-30s (%3d llamadas) %6d↑ %6d↓\n", "TOTAL", totalCalls, totalInput, totalOutput))
		b.WriteString(fmt.Sprintf("  Peticiones: %d al primario %s vs %d al secundario %s. ", primaryCalls, shortLabel(primaryLabel), secondaryCalls, shortLabel(secondaryLabel)))
		if savingsUSD > 0 {
			b.WriteString("↘ Hay AHORRO.\n")
		} else {
			b.WriteString("↖ Sin ahorro (coste real = coste de usar solo el primario).\n")
		}
		b.WriteString(fmt.Sprintf("  Coste REAL (con router):                  $%.4f\n", accruingCost))
		b.WriteString(fmt.Sprintf("  Coste hipotético si TODO con %s: $%.4f\n",
			shortLabel(primaryLabel), hypotheticalCost))
		b.WriteString(fmt.Sprintf("  Ahorro:            $%.4f (%.1f%%)\n",
			savingsUSD, savingsPercent(hypotheticalCost, savingsUSD)))
	} else {
		b.WriteString(fmt.Sprintf("  Coste total: $%.4f (no se pudo estimar ahorro: tarifa de %q no encontrada)\n",
			accruingCost, primaryLabel))
	}

	return savingsUSD, accruingCost, hypotheticalCost, b.String(), primaryFound
}

// savingsReport es la implementación interna compartida (devuelve 4 valores).
func (m *ModelRateMap) savingsReport(usageByModel map[string]UsageRow, primaryLabel string) (savingsUSD, actualCost, hypotheticalCost float64, report string) {
	savingsUSD, actualCost, hypotheticalCost, report, _ = m.SavingsReportFull(usageByModel, primaryLabel, 0, 0, "")
	return
}

// UsageRow es un renglón de consumo para SavingsReport.
type UsageRow struct {
	InputTokens  int
	OutputTokens int
	Label        string
	Calls        int
}

// findRate busca una tarifa por etiqueta exacta o subcadena.
func (m *ModelRateMap) findRate(label string) (Rate, bool) {
	if r, ok := m.rates[label]; ok {
		return r, true
	}
	lower := strings.ToLower(label)
	for key, r := range m.rates {
		if strings.Contains(lower, strings.ToLower(key)) {
			return r, true
		}
		if strings.Contains(strings.ToLower(key), lower) {
			return r, true
		}
	}
	return Rate{}, false
}

func shortLabel(label string) string {
	// Extrae el nombre del modelo de etiquetas como "Claude (claude-sonnet-5)"
	if i := strings.Index(label, "("); i >= 0 {
		j := strings.Index(label[i:], ")")
		if j > 0 {
			return label[i+1 : i+j]
		}
	}
	if len(label) > 30 {
		return label[:27] + "..."
	}
	return label
}

func savingsPercent(hypothetical, savings float64) float64 {
	if hypothetical <= 0 {
		return 0
	}
	p := savings / hypothetical * 100
	if p > 100 {
		return 100
	}
	return math.Round(p*10) / 10
}

// defaultRates devuelve las tarifas por defecto conocidas (julio 2026).
func defaultRates() map[string]Rate {
	return map[string]Rate{
		// --- Anthropic ---
		"Claude (claude-fable-5)":   {Input: 10.00, Output: 50.00},
		"Claude (claude-mythos-5)":  {Input: 10.00, Output: 50.00},
		"Claude (claude-opus-5)":    {Input: 5.00, Output: 25.00},
		"Claude (claude-opus-4-8)":  {Input: 5.00, Output: 25.00},
		"Claude (claude-sonnet-5)":  {Input: 2.00, Output: 10.00},
		"Claude (claude-sonnet-4)":  {Input: 2.00, Output: 10.00},
		"Claude (claude-haiku-45)":  {Input: 1.00, Output: 5.00},
		"Claude (claude-haiku-4-5)": {Input: 1.00, Output: 5.00},
		"Claude (claude-haiku-35)":  {Input: 0.80, Output: 4.00},
		"Claude (claude-haiku-3-5)": {Input: 0.80, Output: 4.00},

		// --- DeepSeek ---
		"DeepSeek (deepseek-chat)":     {Input: 0.435, Output: 0.87},
		"DeepSeek (deepseek-v4-pro)":   {Input: 0.435, Output: 0.87},
		"DeepSeek (deepseek-v4-flash)": {Input: 0.14, Output: 0.28},
		"DeepSeek (deepseek-reasoner)": {Input: 0.55, Output: 2.19},
		"DeepSeek (deepseek-r1)":       {Input: 0.55, Output: 2.19},

		// --- Ollama (local: coste ~0, pero ponemos token simbólico para contabilidad) ---
		// Los modelos locales son esencialmente gratis; el coste real es electricidad.
		// Ponemos un valor simbólico mínimo (~$0.003/M = electricidad estimada).
		"Ollama (gemma4:31b)":    {Input: 0.003, Output: 0.003},
		"Ollama (gemma3:27b)":    {Input: 0.003, Output: 0.003},
		"Ollama (gemma3:12b)":    {Input: 0.002, Output: 0.002},
		"Ollama (qwen3:32b)":     {Input: 0.003, Output: 0.003},
		"Ollama (qwen3:14b)":     {Input: 0.002, Output: 0.002},
		"Ollama (qwen3:8b)":      {Input: 0.001, Output: 0.001},
		"Ollama (llama3.3:70b)":  {Input: 0.004, Output: 0.004},
		"Ollama (llama3.1:8b)":   {Input: 0.001, Output: 0.001},
		"Ollama (mistral:7b)":    {Input: 0.001, Output: 0.001},
		"Ollama (codestral:22b)": {Input: 0.002, Output: 0.002},
	}
}
