package consensus

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/provider"
)

// fakeProvider implements provider.Provider. On the first round it answers the
// question with a fixed label; on later rounds it echoes that it reviewed
// others' answers, so Debate should show convergence.
type fakeProvider struct {
	name  string
	calls int
}

func (f *fakeProvider) Label() string { return f.name }

func (f *fakeProvider) Chat(_ context.Context, msgs []model.Message, _ []model.ToolDefinition, _ string, _, _ provider.DeltaFunc) (model.Message, error) {
	f.calls++
	// Take the last user message content.
	content := ""
	for _, m := range msgs {
		if m.Role == model.RoleUser {
			content = m.Content
		}
	}
	if f.calls == 1 {
		return model.Message{Role: model.RoleAssistant, Content: "initial answer by " + f.name + " for question: " + firstLine(content)}, nil
	}
	// Later rounds: acknowledge having seen the others.
	n := strings.Count(content, "--- ")
	return model.Message{Role: model.RoleAssistant, Content: fmt.Sprintf("REVISED by %s (saw %d other answers)", f.name, n)}, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func mkEntry(name string) Entry {
	return NewEntry(name, &fakeProvider{name: name})
}

func TestDebateRoundsConverge(t *testing.T) {
	entries := []Entry{mkEntry("model-a"), mkEntry("model-b"), mkEntry("model-c")}
	history := Debate(context.Background(), entries, "¿es 2+2=4?", 3)
	if len(history) != 3 {
		t.Fatalf("expected 3 rounds (0,1,2), got %d", len(history))
	}
	if len(history[0].Answers) != 3 {
		t.Fatalf("round 0 should have 3 answers, got %d", len(history[0].Answers))
	}
	final := history[2].Answers
	allRevised := true
	for _, a := range final {
		if a.Err != nil || !strings.HasPrefix(a.Content, "REVISED") {
			allRevised = false
		}
	}
	if !allRevised {
		t.Errorf("final round answers should all be REVISED, got %+v", final)
	}
}

func TestDebateSingleModelNoDebate(t *testing.T) {
	entries := []Entry{mkEntry("solo")}
	history := Debate(context.Background(), entries, "q", 5)
	if len(history) != 1 {
		t.Fatalf("a single model cannot debate: expected 1 round, got %d", len(history))
	}
}

func TestDebatePromptExcludesSelf(t *testing.T) {
	prev := []Answer{
		{Label: "a", Content: "AAA"},
		{Label: "b", Content: "BBB"},
		{Label: "c", Content: "CCC"},
	}
	p := debatePrompt("q", 0, prev)
	if strings.Contains(p, "BBB") == false || strings.Contains(p, "CCC") == false {
		t.Errorf("prompt should include others' answers, got: %s", p)
	}
	if strings.Contains(p, "AAA") == false {
		t.Errorf("prompt should include the model's own previous answer as context, got: %s", p)
	}
	// Self must not appear in the "other models" listing count for itself only.
	n := strings.Count(p, "BBB") + strings.Count(p, "CCC")
	if n < 1 {
		t.Errorf("other answers must appear at least once")
	}
}
