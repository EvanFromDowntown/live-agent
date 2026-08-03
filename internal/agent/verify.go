package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"liveagent/internal/domain"
	"liveagent/internal/llm"
)

const (
	// artifactTotalBudget bounds the total bytes of produced-file content shown
	// to the verifier; artifactPerFile bounds any single file's head.
	artifactTotalBudget = 12000
	artifactPerFile     = 4000
	artifactMaxFiles    = 40
)

// verifyFinish independently judges whether the task's success criteria are
// ACTUALLY met, given the agent's finish summary plus real evidence (workspace
// files + recent transcript). It is an LLM-as-judge: a separate, skeptical call
// that turns the self-reported success signal into something meaningful.
//
// Returns (verified, decided, reason). decided=false means the verifier could
// not be reached / parsed; the caller then accepts the finish but marks it
// unverified rather than blocking on a transient failure (fail-open).
func (a *Agent) verifyFinish(ctx context.Context, summary string) (verified, decided bool, reason string) {
	system := "You are a STRICT verifier. Given a task, the agent's finish summary, the workspace file listing, " +
		"the actual CONTENTS of the produced files, and recent execution evidence, decide whether the task's " +
		"stated success criteria are ACTUALLY met. Inspect the file contents directly — do not rely on the " +
		"agent's claims. If the required output is missing, empty, placeholder, malformed, or fails a stated " +
		"threshold (count/length/format), it is NOT met. Respond with ONLY a JSON object: " +
		`{"verified": true|false, "reason": "<one concise sentence>"}.`

	task := strings.TrimSpace(a.turnTask)
	if task == "" {
		task = a.task
	}
	user := "TASK:\n" + task +
		"\n\nAGENT FINISH SUMMARY:\n" + summary +
		"\n\nWORKSPACE FILES:\n" + a.listWorkspace() +
		"\n\nPRODUCED FILE CONTENTS:\n" + a.readArtifacts() +
		"\n\nRECENT EVIDENCE (transcript tail):\n" + a.tx.tail(4000)

	req := domain.LLMRequest{
		Messages:    []domain.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Temperature: 0,
		MaxTokens:   512,
	}
	resp, err := a.llm.Generate(ctx, req)
	if err != nil {
		return true, false, "verifier unavailable: " + err.Error()
	}
	a.addUsage(a.step, resp)
	obj, err := llm.ExtractJSONObject(resp.Text)
	if err != nil {
		return true, false, "verifier output unparseable"
	}
	var out struct {
		Verified bool   `json:"verified"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(obj), &out); err != nil {
		return true, false, "verifier output unparseable"
	}
	if out.Reason == "" {
		out.Reason = "no reason given"
	}
	return out.Verified, true, out.Reason
}

// readArtifacts returns the actual contents (heads) of the text files the agent
// produced, so the verifier can inspect real output rather than trusting the
// summary. It prunes noise directories, skips hidden/temp and binary files,
// prioritises data-like files, and stays within a byte budget.
func (a *Agent) readArtifacts() string {
	skipDirs := map[string]bool{
		"node_modules": true, ".git": true, ".cache": true, "__pycache__": true,
		"dist": true, "build": true, ".venv": true, "venv": true,
	}
	type cand struct {
		path, rel string
		size      int64
	}
	var cands []cand
	_ = filepath.WalkDir(a.workdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == a.workdir {
				return nil
			}
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") { // hidden incl. the temp .agent_step.py
			return nil
		}
		rel, _ := filepath.Rel(a.workdir, p)
		var sz int64
		if info, e := d.Info(); e == nil {
			sz = info.Size()
		}
		cands = append(cands, cand{path: p, rel: rel, size: sz})
		return nil
	})

	sort.Slice(cands, func(i, j int) bool {
		pi, pj := artifactPriority(cands[i].rel), artifactPriority(cands[j].rel)
		if pi != pj {
			return pi < pj
		}
		return cands[i].size < cands[j].size
	})

	var out strings.Builder
	total, n := 0, 0
	for _, c := range cands {
		if total >= artifactTotalBudget || n >= artifactMaxFiles {
			break
		}
		data, err := os.ReadFile(c.path)
		if err != nil {
			continue
		}
		if looksBinary(data) {
			out.WriteString(fmt.Sprintf("--- %s (%d bytes, binary, skipped) ---\n", c.rel, c.size))
			n++
			continue
		}
		head := string(data)
		if len(head) > artifactPerFile {
			head = head[:artifactPerFile] + fmt.Sprintf("\n...[+%d bytes truncated]", len(data)-artifactPerFile)
		}
		block := fmt.Sprintf("--- %s (%d bytes) ---\n%s\n", c.rel, c.size, head)
		if total+len(block) > artifactTotalBudget {
			if rem := artifactTotalBudget - total; rem > 200 {
				out.WriteString(block[:rem])
			}
			break
		}
		out.WriteString(block)
		total += len(block)
		n++
	}
	if out.Len() == 0 {
		return "(no readable text artifacts)"
	}
	return out.String()
}

// artifactPriority ranks files so likely output artifacts are shown first.
func artifactPriority(rel string) int {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".json", ".ndjson", ".csv", ".tsv", ".txt", ".md", ".yaml", ".yml", ".xml":
		return 0
	case ".html", ".htm", ".log":
		return 2 // often large/noisy
	default:
		return 1
	}
}

// looksBinary reports whether the leading bytes contain a NUL or too many
// non-text bytes to be worth showing.
func looksBinary(data []byte) bool {
	n := len(data)
	if n > 1024 {
		n = 1024
	}
	nonText := 0
	for i := 0; i < n; i++ {
		b := data[i]
		if b == 0 {
			return true
		}
		if b < 0x09 || (b > 0x0d && b < 0x20) {
			nonText++
		}
	}
	return n > 0 && nonText*100/n > 30
}

// listWorkspace returns a compact listing (name + size) of the working
// directory so the verifier can see what was actually produced.
func (a *Agent) listWorkspace() string {
	entries, err := os.ReadDir(a.workdir)
	if err != nil {
		return "(cannot read workspace: " + err.Error() + ")"
	}
	type fe struct {
		name string
		size int64
		dir  bool
	}
	files := make([]fe, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		var sz int64
		if err == nil {
			sz = info.Size()
		}
		files = append(files, fe{name: e.Name(), size: sz, dir: e.IsDir()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	if len(files) == 0 {
		return "(empty)"
	}
	s := ""
	for _, f := range files {
		if f.dir {
			s += fmt.Sprintf("%s/\n", f.name)
		} else {
			s += fmt.Sprintf("%s (%d bytes)\n", f.name, f.size)
		}
	}
	return s
}
