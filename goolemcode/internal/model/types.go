// Package model define el modelo de datos NEUTRAL, independiente del proveedor.
// El bucle agéntico y el registro de herramientas solo conocen estos tipos;
// ni la API de Anthropic ni el protocolo MCP se filtran fuera de su paquete.
package model

import "encoding/json"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// ToolDefinition es una herramienta ofrecida al LLM. Mutating indica si puede
// alterar el disco / tener efectos (→ confirmación + checkpoint antes de usarla).
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON Schema
	Mutating    bool
	Internal    bool // true = no se ofrece al LLM (solo handlers internos del sistema)
}

// ToolCall es una invocación emitida por el modelo.
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// ToolResult es el resultado de ejecutar un ToolCall.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Usage lleva el consumo de tokens de un turno (para reporte de coste).
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ImageData es una imagen adjunta a un mensaje del usuario (entrada multimodal).
// Media es el tipo MIME (p. ej. "image/png"); Bytes son los datos crudos. Cada
// proveedor la codifica a su formato (Ollama: base64; Anthropic: source base64).
type ImageData struct {
	Media string
	Bytes []byte
}

// Message es un turno de la conversación en forma neutral.
// ProviderRaw guarda los bloques crudos de un turno de asistente (p. ej. los
// content blocks de Claude, incluidos los de thinking) para devolverlos SIN
// modificar: la API de Claude rechaza bloques de thinking alterados.
type Message struct {
	Role        Role
	Content     string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
	StopReason  string
	ProviderRaw json.RawMessage
	Usage       *Usage
	Images      []ImageData // imágenes adjuntas (solo en turnos del usuario)
}
