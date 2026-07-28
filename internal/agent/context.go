package agent

import (
	"context"
	"strings"

	"liveagent/internal/domain"
)

// transcript is the rolling, within-run conversation. Instead of a stateless
// call each step, we keep a multi-turn record of what the agent did (assistant
// turns: the tool call it made) and what happened (user turns: the real tool
// result). The model reads its own last action and the resulting output, and
// corrects itself — the ingredient for self-debugging.
//
// The system prompt and the current "ask" (which carries the pinned env/goal/
// plan context block) are rebuilt fresh every step and are NOT stored here, so
// they never get compacted away.
type transcript struct {
	turns []domain.Message // alternating assistant / user

	maxChars   int
	keepRecent int
	compactAt  int // char threshold that triggers compaction
}

func newTranscript(maxChars, keepRecent int, compactAtPct float64) *transcript {
	if maxChars <= 0 {
		maxChars = 48000
	}
	if keepRecent <= 0 {
		keepRecent = 6
	}
	if compactAtPct <= 0 || compactAtPct > 1 {
		compactAtPct = 0.75
	}
	return &transcript{
		maxChars:   maxChars,
		keepRecent: keepRecent,
		compactAt:  int(float64(maxChars) * compactAtPct),
	}
}

// addAction records the assistant's tool call as an assistant turn.
func (t *transcript) addAction(name, arguments string) {
	content := "TOOL_CALL " + name
	if strings.TrimSpace(arguments) != "" {
		content += " " + arguments
	}
	t.turns = append(t.turns, domain.Message{Role: "assistant", Content: content})
}

// addResult records the execution outcome as a user turn.
func (t *transcript) addResult(text string, isError bool) {
	prefix := "RESULT: "
	if isError {
		prefix = "RESULT (error): "
	}
	t.turns = append(t.turns, domain.Message{Role: "user", Content: prefix + strings.TrimSpace(text)})
}

// tail returns up to maxChars of the most recent transcript turns (oldest-first
// within the window), used as evidence for verification.
func (t *transcript) tail(maxChars int) string {
	if len(t.turns) == 0 {
		return "(none)"
	}
	var parts []string
	total := 0
	for i := len(t.turns) - 1; i >= 0; i-- {
		c := t.turns[i].Role + ": " + t.turns[i].Content
		if total+len(c) > maxChars && len(parts) > 0 {
			break
		}
		parts = append(parts, c)
		total += len(c)
	}
	// reverse to chronological order
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "\n")
}

func (t *transcript) chars() int {
	n := 0
	for _, m := range t.turns {
		n += len(m.Content)
	}
	return n
}

// build assembles the message list for one call: fresh system prompt, the
// retained transcript, then the current ask last.
func (t *transcript) build(system, ask string) []domain.Message {
	msgs := make([]domain.Message, 0, len(t.turns)+2)
	msgs = append(msgs, domain.Message{Role: "system", Content: system})
	msgs = append(msgs, t.turns...)
	msgs = append(msgs, domain.Message{Role: "user", Content: ask})
	return msgs
}

// summarizer compresses dropped turns into a short paragraph.
type summarizer func(ctx context.Context, text string) string

// compactIfNeeded folds the oldest turns (those before the keepRecent tail) into
// ONE summary turn once the transcript passes its char threshold, preserving the
// recent tail intact. Degrades to coarse truncation if summarization fails.
func (t *transcript) compactIfNeeded(ctx context.Context, sum summarizer) {
	if t.chars() <= t.compactAt || len(t.turns) <= t.keepRecent {
		return
	}
	cut := len(t.turns) - t.keepRecent
	old := t.turns[:cut]
	recent := append([]domain.Message(nil), t.turns[cut:]...)

	var b strings.Builder
	for _, m := range old {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	summary := ""
	if sum != nil {
		summary = strings.TrimSpace(sum(ctx, b.String()))
	}
	if summary == "" {
		summary = truncate(b.String(), 1000)
	}
	head := domain.Message{Role: "user", Content: "EARLIER STEPS (compacted): " + summary}
	t.turns = append([]domain.Message{head}, recent...)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
