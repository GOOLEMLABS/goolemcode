// Package usage lleva la cuenta del consumo de tokens DESGLOSADO POR MODELO.
// Es compartido: el proveedor principal reporta su uso por turno y las
// herramientas que llaman a otros LLM (p. ej. query_deepseek) reportan el suyo,
// de modo que /tokens muestra cuánto ha gastado cada modelo en la sesión.
package usage

import (
	"fmt"
	"strings"
	"sync"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/pricing"
)

type entry struct {
	usage model.Usage
	calls int
}

// Tracker acumula uso por etiqueta de modelo. Seguro para uso concurrente
// (las herramientas pueden ejecutarse desde el bucle del agente).
type Tracker struct {
	mu     sync.Mutex
	models map[string]*entry
	order  []string
	rates  *pricing.ModelRateMap
}

func New() *Tracker {
	return &Tracker{
		models: map[string]*entry{},
		rates:  pricing.NewModelRateMap(),
	}
}

// SetRates reemplaza el mapa de tarifas (útil si el usuario configura precios personalizados).
func (t *Tracker) SetRates(rates *pricing.ModelRateMap) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rates = rates
}

// GetRates devuelve el mapa de tarifas (para que main.go pueda registrarlo).
func (t *Tracker) GetRates() *pricing.ModelRateMap {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rates
}

// Add suma el uso de una llamada a un modelo (no hace nada si es cero).
func (t *Tracker) Add(modelLabel string, u model.Usage) {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.models[modelLabel]
	if e == nil {
		e = &entry{}
		t.models[modelLabel] = e
		t.order = append(t.order, modelLabel)
	}
	e.usage.InputTokens += u.InputTokens
	e.usage.OutputTokens += u.OutputTokens
	e.calls++
}

// Report devuelve una tabla legible del consumo por modelo (con coste estimado), o "" si no hay uso.
func (t *Tracker) Report() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.order) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Consumo de tokens por modelo (sesión):\n")
	var tin, tout int
	var totalCost float64
	rates := t.rates
	for _, name := range t.order {
		e := t.models[name]
		var costStr string
		cost, found := rates.Cost(name, e.usage.InputTokens, e.usage.OutputTokens)
		if found {
			costStr = fmt.Sprintf("  $%.4f", cost)
			totalCost += cost
		}
		fmt.Fprintf(&b, "  %-28s %6d↑  %6d↓   (%d llamadas)%s\n",
			name, e.usage.InputTokens, e.usage.OutputTokens, e.calls, costStr)
		tin += e.usage.InputTokens
		tout += e.usage.OutputTokens
	}
	if len(t.order) > 1 {
		fmt.Fprintf(&b, "  %-28s %6d↑  %6d↓   $%.4f\n", "TOTAL", tin, tout, totalCost)
	} else {
		fmt.Fprintf(&b, "  Coste total estimado: $%.4f\n", totalCost)
	}
	return strings.TrimRight(b.String(), "\n")
}

// RawByModel devuelve el consumo desglosado por modelo (para cálculos externos).
func (t *Tracker) RawByModel() map[string]pricing.UsageRow {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]pricing.UsageRow, len(t.order))
	for _, name := range t.order {
		e := t.models[name]
		out[name] = pricing.UsageRow{
			Label:        name,
			InputTokens:  e.usage.InputTokens,
			OutputTokens: e.usage.OutputTokens,
			Calls:        e.calls,
		}
	}
	return out
}

// Order devuelve los nombres de los modelos en el orden en que se usaron.
func (t *Tracker) Order() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.order))
	copy(out, t.order)
	return out
}

// ReportSavings devuelve un informe de ahorro estimado si hay smart routing,
// o "" si no hay uso o el mapa de tarifas no tiene datos.
// primaryLabel es la etiqueta del modelo primario (el más caro).
func (t *Tracker) ReportSavings(primaryLabel string) string {
	t.mu.Lock()
	usageByModel := make(map[string]pricing.UsageRow, len(t.order))
	for _, name := range t.order {
		e := t.models[name]
		usageByModel[name] = pricing.UsageRow{
			Label:        name,
			InputTokens:  e.usage.InputTokens,
			OutputTokens: e.usage.OutputTokens,
			Calls:        e.calls,
		}
	}
	rates := t.rates
	t.mu.Unlock()

	if len(usageByModel) == 0 {
		return ""
	}
	_, _, _, report := rates.SavingsReport(usageByModel, primaryLabel)
	return report
}
