package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"liveagent/internal/domain"
)

// verifyStub answers planning calls (has Tools) with a scripted step, and
// answers verifier calls (no Tools, system mentions "verifier") with a fixed
// verdict. Everything else (e.g. compaction) returns empty text.
type verifyStub struct {
	steps    []domain.LLMResponse
	i        int
	verified bool
	decided  bool
}

func (s *verifyStub) Generate(_ context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	if len(req.Tools) == 0 {
		if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "verifier") {
			if !s.decided {
				return domain.LLMResponse{Text: "sorry, cannot tell"}, nil
			}
			return domain.LLMResponse{Text: fmt.Sprintf(`{"verified": %v, "reason": "test verdict"}`, s.verified)}, nil
		}
		return domain.LLMResponse{Text: ""}, nil
	}
	if s.i < len(s.steps) {
		r := s.steps[s.i]
		s.i++
		return r, nil
	}
	s.i++
	return call("finish", `{"summary":"claim done","success":true}`), nil
}

func enableVerify(a *Agent, maxRej int) {
	on := true
	a.cfg.Verify.Enabled = &on
	a.cfg.Verify.MaxRejections = maxRej
}

func TestVerifierAcceptsTrue(t *testing.T) {
	stub := &verifyStub{decided: true, verified: true, steps: []domain.LLMResponse{
		call("finish", `{"summary":"did it","success":true}`),
	}}
	ag := newTestAgent(t, stub)
	enableVerify(ag, 2)
	res, _ := ag.Run(context.Background(), "do it")
	if !res.Finished || !res.Success || !res.Verified {
		t.Fatalf("expected verified success, got %+v", res)
	}
}

func TestVerifierRejectsUntilExhausted(t *testing.T) {
	// Verifier always says NOT met; every model turn finishes → after MaxRejections
	// the finish is accepted as unverified (success=false).
	stub := &verifyStub{decided: true, verified: false}
	ag := newTestAgent(t, stub)
	enableVerify(ag, 2)
	res, _ := ag.Run(context.Background(), "do it")
	if res.StopReason != "finish_unverified" || res.Success || res.Verified {
		t.Fatalf("expected finish_unverified/failed, got %+v", res)
	}
}

func TestVerifierUndecidedFailsOpen(t *testing.T) {
	// Verifier unparseable → accept finish but mark unverified.
	stub := &verifyStub{decided: false, steps: []domain.LLMResponse{
		call("finish", `{"summary":"did it","success":true}`),
	}}
	ag := newTestAgent(t, stub)
	enableVerify(ag, 2)
	res, _ := ag.Run(context.Background(), "do it")
	if !res.Finished || !res.Success || res.Verified {
		t.Fatalf("expected success but unverified, got %+v", res)
	}
}

func TestReadArtifacts(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	w := ag.workdir
	writeFile := func(rel, content string) {
		p := filepath.Join(w, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("result.json", `[{"author":"A","content":"hello world this is long enough"}]`)
	writeFile("notes.log", "some log line\n")
	writeFile("blob.bin", "abc\x00\x00\x00binary")
	writeFile("node_modules/pkg/index.js", "should be pruned")
	writeFile(".agent_step.py", "print('temp, hidden, skipped')")

	out := ag.readArtifacts()
	if !strings.Contains(out, "result.json") || !strings.Contains(out, "hello world this is long enough") {
		t.Fatalf("expected result.json content, got:\n%s", out)
	}
	if !strings.Contains(out, "binary, skipped") {
		t.Fatalf("expected binary file to be skipped-marked, got:\n%s", out)
	}
	if strings.Contains(out, "should be pruned") || strings.Contains(out, "node_modules") {
		t.Fatalf("node_modules must be pruned, got:\n%s", out)
	}
	if strings.Contains(out, "temp, hidden, skipped") {
		t.Fatalf("hidden temp file must be skipped, got:\n%s", out)
	}
	// data-like (.json) must be listed before noisy (.log)
	if ji, li := strings.Index(out, "result.json"), strings.Index(out, "notes.log"); ji < 0 || li < 0 || ji > li {
		t.Fatalf("expected result.json before notes.log, got:\n%s", out)
	}
}

func TestStallNudgeAppears(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ag.task = "x"
	ag.env = map[string]any{"os": "test"}
	ag.cfg.Limits.StallNudge = 3
	ag.stallStreak = 3
	if !strings.Contains(ag.buildAsk(), "WARNING") {
		t.Fatal("expected stall warning in ask when streak >= nudge threshold")
	}
	ag.stallStreak = 1
	if strings.Contains(ag.buildAsk(), "WARNING") {
		t.Fatal("did not expect warning below threshold")
	}
}
