// Package consensus implementa el "modo consenso": pregunta a varios LLM
// (de los configurados: DeepSeek, Claude, y los modelos del servidor Ollama)
// por el mismo planteamiento y determina si llegan a la misma conclusión.
//
// Flujo: se seleccionan N modelos (>=2), se lanza la pregunta a todos en
// paralelo (ronda 0, sin verse entre ellos) y luego se DEBATEN varias rondas
// compartiendo las respuestas de todos: en cada ronda cada modelo revisa su
// postura a la luz de las de los demás, hasta converger. El modelo "juez" (el
// principal) solo resume al final el resultado del debate; no decide en solitario.
package consensus

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/provider"
)

// Entry es un LLM disponible para consenso, con su origen (deepseek/claude/ollama).
type Entry struct {
	Name   string // nombre en el catálogo (p. ej. "deepseek-v4-pro", "gemma4:31b")
	Label  string // etiqueta amigable (incluye el origen)
	Origin string // "DeepSeek API" | "Claude" | "Ollama"
	prov   provider.Provider
}

// Available extrae el origen de una etiqueta de proveedor. Devuelve una cadena
// corta y legible ("DeepSeek API", "Claude", "Ollama"…).
func OriginFromLabel(label string) string {
	switch {
	case strings.HasPrefix(label, "DeepSeek"):
		return "DeepSeek API"
	case strings.HasPrefix(label, "Claude"):
		return "Claude"
	case strings.HasPrefix(label, "Ollama"):
		return "Ollama"
	}
	return "otro"
}

// NewEntry envuelve un proveedor en una entrada de consenso.
func NewEntry(name string, p provider.Provider) Entry {
	prov := p
	return Entry{Name: name, Label: prov.Label(), Origin: OriginFromLabel(prov.Label()), prov: prov}
}

// Answer es la respuesta de un LLM a la pregunta.
type Answer struct {
	Label   string
	Origin  string
	Content string
	Err     error
}

// AskAll lanza question a todas las entradas en paralelo y devuelve sus
// respuestas. Cada respuesta fallida lleva Err relleno.
func AskAll(ctx context.Context, entries []Entry, question string) []Answer {
	if len(entries) == 0 {
		return nil
	}
	answers := make([]Answer, len(entries))
	var wg sync.WaitGroup
	for i := range entries {
		e := entries[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs := []model.Message{{Role: model.RoleUser, Content: question}}
			resp, err := e.prov.Chat(ctx, msgs, nil, "", nil, nil)
			answers[i] = Answer{Label: e.Label, Origin: e.Origin, Content: resp.Content, Err: err}
		}()
	}
	wg.Wait()
	sort.SliceStable(answers, func(a, b int) bool { return answers[a].Label < answers[b].Label })
	return answers
}

// RenderAnswers devuelve un texto legible con las respuestas (numeradas por modelo),
// listo para pasar al juez.
func RenderAnswers(question string, answers []Answer) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "QUESTION/CONSENSO: %s\n\n", question)
	for i, a := range answers {
		fmt.Fprintf(&sb, "===== %d) %s (%s) =====\n", i+1, a.Label, a.Origin)
		if a.Err != nil {
			fmt.Fprintf(&sb, "[error: %v]\n", a.Err)
		} else {
			sb.WriteString(a.Content)
		}
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// judgePrompt instruye al juez a decidir si hay consenso y resumir.
const judgePrompt = `You are the debate judge. Several AI models were independently asked the same
question. Your task: determine whether they REACH THE SAME CONCLUSION (consensus).

Analyze the answers below and return a concise report:
1. CONSENSUS: reach consensus / partial consensus / no consensus (disagreement)
   - Briefly explain why.
2. KEY AGREEMENTS: what most/all models agree on.
3. DIFFERENCES: where they diverge.
4. Final answer in 2-3 sentences (in the same language as the question).

Answers:
%s`

// Judge pide al modelo juez que analice las respuestas y determine el consenso.
func Judge(ctx context.Context, judge provider.Provider, question string, answers []Answer) (string, error) {
	msgs := []model.Message{{Role: model.RoleUser, Content: fmt.Sprintf(judgePrompt, RenderAnswers(question, answers))}}
	resp, err := judge.Chat(ctx, msgs, nil, "", nil, nil)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// RoundResult es el resultado de una ronda de debate: las respuestas de ese
// round para cada entrada (en el mismo orden que las entradas).
type RoundResult struct {
	Round   int
	Answers []Answer
}

// Debate ejecuta un debate en rondas entre los modelos seleccionados. En la
// ronda 0 cada modelo responde la pregunta por separado SIN ver a los demás.
// En cada ronda siguiente se comparten TODAS las respuestas de la ronda
// anterior con todos los modelos, para que puedan revisar su postura a la luz
// de lo que dijeron los demás (multilateral). Así converge hacia un veredicto
// conjunto: el juez solo interviene al final para resumir (ver Judge).
//
// Devuelve el historial de rondas completo. La última entrada de history es la
// ronda final (la que debe juzgarse); la primera es la respuesta inicial, útil
// para medir cuánto convergieron.
func Debate(ctx context.Context, entries []Entry, question string, rounds int) []RoundResult {
	if rounds < 1 {
		rounds = 1
	}
	ans := AskAll(ctx, entries, question)
	if len(ans) == 0 {
		return nil
	}
	history := []RoundResult{{Round: 0, Answers: ans}}
	if len(ans) == 1 || rounds == 1 {
		// Un solo modelo no puede debatir: devolvemos solo la ronda inicial.
		return history
	}
	current := ans
	for round := 1; round < rounds; round++ {
		current = debateRound(ctx, entries, question, current)
		history = append(history, RoundResult{Round: round, Answers: current})
	}
	return history
}

// debateRound envía a cada modelo las respuestas de la ronda anterior de los
// DEMÁS (excluyendo las suyas propias) y le pide que responda de nuevo, pudiendo
// mantener o revisar su postura. Devuelve las respuestas revisadas.
func debateRound(ctx context.Context, entries []Entry, question string, prev []Answer) []Answer {
	results := make([]Answer, len(entries))
	var wg sync.WaitGroup
	for i := range entries {
		e := entries[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs := []model.Message{{Role: model.RoleUser, Content: debatePrompt(question, i, prev)}}
			resp, err := e.prov.Chat(ctx, msgs, nil, "", nil, nil)
			results[i] = Answer{Label: e.Label, Origin: e.Origin, Content: resp.Content, Err: err}
		}()
	}
	wg.Wait()
	return results
}

// debatePrompt construye el mensaje para un modelo (índice self) con las
// respuestas de los demás de la ronda anterior. Su propia respuesta anterior se
// le incluye como contexto de qué dijo, pero se le pide responder de nuevo.
func debatePrompt(question string, self int, prev []Answer) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "QUESTION: %s\n\n", question)
	fmt.Fprintf(&sb, "Below are the answers given by the OTHER models in the previous round of a debate.\n")
	fmt.Fprintf(&sb, "Your own previous answer was:\n\n")
	if prev[self].Err != nil {
		fmt.Fprintf(&sb, "[your previous answer errored: %v]\n", prev[self].Err)
	} else if prev[self].Content != "" {
		sb.WriteString(prev[self].Content)
	} else {
		sb.WriteString("[none]\n")
	}
	sb.WriteString("\n\nANSWERS FROM THE OTHER MODELS (previous round):\n")
	for i, a := range prev {
		if i == self {
			continue
		}
		fmt.Fprintf(&sb, "\n--- %s (%s) ---\n", a.Label, a.Origin)
		if a.Err != nil {
			fmt.Fprintf(&sb, "[error: %v]\n", a.Err)
		} else {
			sb.WriteString(a.Content)
		}
		sb.WriteString("\n")
	}
	sb.WriteString(`
Now give your REVISED final answer to the original question. You may keep or
change your previous position. Consider the points raised by the other models
and explain briefly if you changed your mind. End with a clear FINAL ANSWER.
Do not address the other models; just produce your own best answer.`)
	return sb.String()
}
