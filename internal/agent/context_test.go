package agent

import (
	"context"
	"strings"
	"testing"
)

func TestTranscriptBuildOrder(t *testing.T) {
	tx := newTranscript(0, 0, 0)
	tx.addAction("run_shell", `{"command":"ls"}`)
	tx.addResult("file.txt", false)
	msgs := tx.build("SYS", "ASK")
	if msgs[0].Role != "system" || msgs[0].Content != "SYS" {
		t.Fatalf("first message must be system")
	}
	if msgs[len(msgs)-1].Role != "user" || msgs[len(msgs)-1].Content != "ASK" {
		t.Fatalf("last message must be the ask")
	}
	if msgs[1].Role != "assistant" || !strings.Contains(msgs[1].Content, "run_shell") {
		t.Fatalf("expected assistant tool-call turn, got %+v", msgs[1])
	}
}

func TestCompaction(t *testing.T) {
	tx := newTranscript(200, 2, 0.5) // tiny budget forces compaction
	for i := 0; i < 10; i++ {
		tx.addAction("run_shell", strings.Repeat("x", 40))
		tx.addResult(strings.Repeat("y", 40), false)
	}
	before := len(tx.turns)
	tx.compactIfNeeded(context.Background(), func(context.Context, string) string { return "SUMMARY" })
	if len(tx.turns) >= before {
		t.Fatalf("expected compaction to reduce turns: before=%d after=%d", before, len(tx.turns))
	}
	if !strings.Contains(tx.turns[0].Content, "SUMMARY") {
		t.Fatalf("expected compacted head to contain summary, got %q", tx.turns[0].Content)
	}
}
