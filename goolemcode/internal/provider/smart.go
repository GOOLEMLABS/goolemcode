package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

type SmartRouter struct {
	primary        Provider
	secondary      Provider
	primaryBetter  bool
	lastLabel      string
	mu             sync.Mutex
	primaryCalls   int
	secondaryCalls int

	// ConsecutiveFallback es el número de veces consecutivas que el primario ha
	// fallado antes de preguntar al usuario (0 = desactivado; por defecto 3).
	consecutiveFallback int
	primaryFails        int

	// askUser, si no es nil, se invoca tras `consecutiveFallback` fallos seguidos
	// del primario para que el usuario decida si se pasa al secundario. Devuelve
	// true si el usuario confirma el cambio a secundario.
	askUser func(message string) bool
}

func NewSmartRouter(primary, secondary Provider, primaryBetter bool) *SmartRouter {
	return &SmartRouter{primary: primary, secondary: secondary, primaryBetter: primaryBetter}
}

// SetAskUser registra el callback que consulta al usuario tras varios fallos
// consecutivos del primario.
func (s *SmartRouter) SetAskUser(fn func(message string) bool) { s.askUser = fn }

// SetConsecutiveFallback define cuántos fallos seguidos del primario disparan la
// consulta al usuario (0 desactiva la consulta).
func (s *SmartRouter) SetConsecutiveFallback(n int) {
	if n > 0 {
		s.consecutiveFallback = n
	}
}

func (s *SmartRouter) Label() string {
	arrow := "<"
	if s.primaryBetter {
		arrow = ">"
	}
	return fmt.Sprintf("↦ %s %s %s", s.primary.Label(), arrow, s.secondary.Label())
}

func (s *SmartRouter) SetPrimary(p Provider) { s.primary = p }

// PrimaryLabel devuelve la etiqueta del modelo primario (el que se considera
// "caro" para calcular el ahorro del Smart Router).
func (s *SmartRouter) PrimaryLabel() string {
	return s.primary.Label()
}

// SecondaryLabel devuelve la etiqueta del modelo secundario (el barato).
func (s *SmartRouter) SecondaryLabel() string {
	return s.secondary.Label()
}

// IsPrimaryLabel indica si la etiqueta se corresponde con el modelo primario.
func (s *SmartRouter) IsPrimaryLabel(label string) bool {
	return label != "" && label == s.primary.Label()
}

// IsSecondaryLabel indica si la etiqueta se corresponde con el modelo secundario.
func (s *SmartRouter) IsSecondaryLabel(label string) bool {
	return label != "" && label == s.secondary.Label()
}

func (s *SmartRouter) Chat(ctx context.Context, messages []model.Message, tools []model.ToolDefinition, system string, onDelta, onThinking DeltaFunc) (model.Message, error) {
	chosen := s.pickModel(messages)
	degraded := false

	// Si el turno fue asignado al secundario pero la llamada falla, degradamos
	// al primario (p. ej. Ollama local caída → DeepSeek), en lugar de matar el turno.
	// Solo se hace fallback cuando el error ocurre antes del primer byte útil
	// (doWithRetry reintenta solo estados transitorios y devuelve antes del stream),
	// así que no se corre el riesgo de recomenzar tras ya haber emitido texto.
	if chosen == s.secondary {
		resp, err := chosen.Chat(ctx, messages, tools, system, onDelta, onThinking)
		if err != nil {
			if onDelta != nil {
				onDelta(fmt.Sprintf("\n⚠️ %s no disponible (%v), paso a %s\n[→ %s]\n",
					s.secondary.Label(), err, s.primary.Label(), s.primary.Label()))
			}
			chosen = s.primary
			degraded = true
		} else {
			s.mu.Lock()
			s.lastLabel = chosen.Label()
			s.mu.Unlock()
			return resp, nil
		}
	}

	s.mu.Lock()
	// Error de red/transitorio, no un "no" del modelo: si llevamos varios fallos
	// seguidos del primario, preguntamos al usuario si quiere pasar al secundario.
	var proceed bool
	if chosen == s.primary && s.askUser != nil {
		s.primaryFails++
		if s.consecutiveFallback > 0 && s.primaryFails >= s.consecutiveFallback {
			s.primaryFails = 0
			proceed = s.askUser(fmt.Sprintf(
				"El modelo primario (%s) no ha respondido en %d intentos consecutivos. ¿Paso al secundario (%s)?",
				s.primary.Label(), s.consecutiveFallback, s.secondary.Label()))
			if proceed {
				chosen = s.secondary
				s.secondaryCalls++
				s.lastLabel = chosen.Label()
				s.mu.Unlock()
				if onDelta != nil {
					onDelta(fmt.Sprintf("\n[→ %s]\n", chosen.Label()))
				}
				return chosen.Chat(ctx, messages, tools, system, onDelta, onThinking)
			}
		}
	} else {
		// Reset: el primario respondió bien (o no hay flujo de preguntas).
		s.primaryFails = 0
	}
	if chosen == s.primary {
		s.primaryCalls++
	} else {
		s.secondaryCalls++
	}
	s.lastLabel = chosen.Label()
	s.mu.Unlock()

	// El aviso "[→ X]" del primario con degradación ya se emitió en el mensaje ⚠️.
	if onDelta != nil && !degraded {
		onDelta(fmt.Sprintf("\n[→ %s]\n", chosen.Label()))
	}
	resp, err := chosen.Chat(ctx, messages, tools, system, onDelta, onThinking)
	if err == nil {
		s.mu.Lock()
		s.lastLabel = chosen.Label()
		s.mu.Unlock()
	}
	return resp, err
}

// ActiveLabel devuelve la etiqueta del modelo que respondió al último turno.
func (s *SmartRouter) ActiveLabel() string {
	return s.lastLabel
}

// CallCounts devuelve cuántas llamadas se han hecho a cada modelo: número de
// peticiones enviadas al primario y al secundario desde que se creó el router.
func (s *SmartRouter) CallCounts() (primary, secondary int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.primaryCalls, s.secondaryCalls
}

func (s *SmartRouter) pickModel(messages []model.Message) Provider {
	complex := isComplexTask(messages)
	if s.primaryBetter {
		if complex {
			return s.primary
		}
		return s.secondary
	}
	if complex {
		return s.secondary
	}
	return s.primary
}

func isComplexTask(messages []model.Message) bool {
	lastUser := ""
	hasImages := false
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == model.RoleUser {
			lastUser = messages[i].Content
			hasImages = len(messages[i].Images) > 0
			break
		}
	}
	if lastUser == "" {
		return false
	}

	score := 0

	words := len(strings.Fields(lastUser))
	switch {
	case words > 100:
		score += 3
	case words > 50:
		score += 2
	case words > 20:
		score += 1
	}

	codeBlocks := strings.Count(lastUser, "```")
	score += codeBlocks * 2

	complexKeywords := []string{
		"refactor", "arquitectur", "diseñ", "implement", "optimiz",
		"debug", "explain", "crea", "desarroll", "configur", "deploy",
		"migrat", "analyz", "investig", "architectur", "patrón",
		"multithread", "concurrenc", "asynchron", "parallel", "distribut",
		"rendimient", "escalabilid", "seguridad", "complej",
	}
	lower := strings.ToLower(lastUser)
	for _, kw := range complexKeywords {
		if strings.Contains(lower, kw) {
			score += 2
			break
		}
	}

	if hasImages {
		score += 2
	}

	qCount := strings.Count(lastUser, "?")
	if qCount >= 2 {
		score++
	}

	return score >= 4
}
