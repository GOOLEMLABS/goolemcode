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

// HistoryTrimmer es una interfaz OPCIONAL que un proveedor puede implementar si
// guarda razonamiento (thinking/reasoning) o argumentos crudos en el historial.
// El agente la invoca antes de cada llamada para purgar ese contenido de los
// turnos ya cerrados y no reenviarlo indefinidamente.
//
// El proveedor debe respetar las reglas de su API: p. ej. Anthropic exige
// conservar los bloques de thinking dentro del turno de tool-use en curso
// (keepFrom en adelante), pero permite omitirlos en turnos ya cerrados. Los
// proveedores que no persisten reasoning (DeepSeek, Ollama) no necesitan
// implementarla.
//
// keepFrom es el índice donde empieza el turno actual (el mensaje de contexto
// dinámico o, si no hay, el mensaje del usuario). Todo lo anterior son turnos
// cerrados y se puede purgar.
type HistoryTrimmer interface {
	TrimHistory(messages []model.Message, keepFrom int) []model.Message
}
