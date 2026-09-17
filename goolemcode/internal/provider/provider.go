// Package provider abstrae los backends de LLM. Cada proveedor traduce entre el
// modelo neutral y el formato de cable de su API en su frontera; el resto del
// programa habla solo el modelo neutral.
package provider

import (
	"context"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

// DeltaFunc recibe fragmentos de texto a medida que llegan (streaming).
type DeltaFunc func(string)

type Provider interface {
	// Chat genera el siguiente turno del asistente (texto y/o tool_calls).
	// onDelta recibe el texto de la respuesta; onThinking recibe el razonamiento
	// del modelo (qwen3, deepseek-reasoner, Claude…) si lo emite. Ambos pueden ser
	// nil.
	Chat(ctx context.Context, messages []model.Message, tools []model.ToolDefinition, system string, onDelta, onThinking DeltaFunc) (model.Message, error)
	Label() string
}
