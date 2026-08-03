package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"liveagent/internal/domain"
)

// maxOutput bounds tool output fed back to the model so a single noisy command
// cannot blow the context budget. The middle is elided.
const maxOutput = 8000

// Builtins constructs the standard host tools. Execution is DIRECT on the host
// process (no sandbox) inside Workdir, per the chosen design.
type Builtins struct {
	Workdir   string
	Timeout   time.Duration
	PythonBin string // resolved python interpreter ("python3"/"python")
	http      *http.Client

	svcMu    sync.Mutex
	services map[string]*bgService // long-running background processes started this session
}

// RegisterAll registers every built-in tool into the toolset.
func (b *Builtins) RegisterAll(ts *Toolset) {
	if b.Timeout <= 0 {
		b.Timeout = 120 * time.Second
	}
	if b.PythonBin == "" {
		b.PythonBin = resolvePython()
	}
	b.http = &http.Client{Timeout: b.Timeout}
	ts.Register(&shellTool{b})
	ts.Register(&pythonTool{b})
	ts.Register(&readFileTool{b})
	ts.Register(&writeFileTool{b})
	ts.Register(&editFileTool{b})
	ts.Register(&listDirTool{b})
	ts.Register(&globTool{b})
	ts.Register(&grepTool{b})
	ts.Register(&httpFetchTool{b})
	ts.Register(&startServiceTool{b})
	ts.Register(&listServicesTool{b})
	ts.Register(&stopServiceTool{b})
	ts.Register(&replyTool{})
	ts.Register(&setTitleTool{})
	ts.Register(&sendFileTool{b})
	ts.Register(&updatePlanTool{})
	ts.Register(&finishTool{})
}

func resolvePython() string {
	for _, p := range []string{"python3", "python"} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return "python3"
}

// outputSink receives incremental stdout/stderr chunks from a streaming command
// so callers (the agent → web UI) can show long-running output as it is
// produced instead of only at the end.
type outputSink func(chunk string)

type sinkKey struct{}

// WithOutputSink attaches a streaming-output callback to ctx; run_shell and
// run_python invoke it with each output chunk as it arrives.
func WithOutputSink(ctx context.Context, fn outputSink) context.Context {
	return context.WithValue(ctx, sinkKey{}, fn)
}

func sinkFrom(ctx context.Context) outputSink {
	if fn, ok := ctx.Value(sinkKey{}).(outputSink); ok {
		return fn
	}
	return nil
}

// runCmd executes a command in Workdir with a timeout, returning combined
// stdout+stderr and whether it failed. If a streaming sink is attached to ctx,
// output is forwarded chunk-by-chunk as it is produced.
func (b *Builtins) runCmd(ctx context.Context, name string, args ...string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = b.Workdir

	sink := sinkFrom(ctx)
	if sink == nil {
		// Fast path: no live streaming requested.
		out, err := cmd.CombinedOutput()
		return finishCmdOutput(cctx, string(out), err, b.Timeout)
	}

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return "cannot start command: " + err.Error(), true
	}
	var buf strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := bufio.NewReader(pr)
		const streamCap = 64 * 1024 // bound how much we forward to the UI
		streamed := 0
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				buf.WriteString(line)
				if streamed < streamCap {
					sink(line)
					streamed += len(line)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	werr := cmd.Wait()
	pw.Close()
	<-done
	return finishCmdOutput(cctx, buf.String(), werr, b.Timeout)
}

// finishCmdOutput applies the shared clip / timeout / exit-status formatting to
// a command's combined output.
func finishCmdOutput(ctx context.Context, raw string, err error, timeout time.Duration) (string, bool) {
	s := clip(raw)
	if ctx.Err() == context.DeadlineExceeded {
		return s + fmt.Sprintf("\n[timed out after %s]", timeout), true
	}
	if err != nil {
		return fmt.Sprintf("%s\n[exit: %v]", s, err), true
	}
	if strings.TrimSpace(s) == "" {
		s = "[no output; exit 0]"
	}
	return s, false
}

func clip(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	head := s[:maxOutput*2/3]
	tail := s[len(s)-maxOutput/3:]
	return head + fmt.Sprintf("\n...[%d bytes elided]...\n", len(s)-len(head)-len(tail)) + tail
}

// resolve turns a possibly-relative path into one anchored under Workdir.
func (b *Builtins) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(b.Workdir, p)
}

// resolveJailed is like resolve but refuses paths that escape the workspace
// (via absolute paths or ".." traversal). File tools use it so the agent cannot
// read or clobber files outside its sandboxed working directory. Shell/python
// are intentionally NOT jailed (only the dangerous-command guard applies there).
func (b *Builtins) resolveJailed(p string) (string, error) {
	full := filepath.Clean(b.resolve(p))
	root := filepath.Clean(b.Workdir)
	if full == root || strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return full, nil
	}
	return "", fmt.Errorf("path %q is outside the workspace and is blocked", p)
}

func str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func strOr(args map[string]any, key, def string) string {
	if s, ok := args[key].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return def
}

// intArg reads an integer argument. JSON numbers decode to float64.
func intArg(args map[string]any, key string, def int) int {
	switch n := args[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return def
}

// isSkippedDir reports directories we never descend into for glob/grep/list, to
// avoid drowning results in dependency and VCS noise.
func isSkippedDir(name string) bool {
	switch name {
	case ".git", "node_modules", "__pycache__", ".venv", "venv", ".idea", ".mypy_cache", ".pytest_cache", "dist", "build", ".next":
		return true
	}
	return false
}

// isBinary heuristically detects binary content (a NUL byte in the first 8 KiB).
func isBinary(data []byte) bool {
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// globToRegexp compiles a shell-style glob into an anchored RE2 regexp.
// Supported: '*' (any run within a segment), '**' (any depth; '**/' also matches
// zero directories), '?' (one non-separator char). Paths use forward slashes.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++ // consume second '*'
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++ // consume '/'
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '(', ')', '+', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// -----------------------------------------------------------------------------
// run_shell
// -----------------------------------------------------------------------------

type shellTool struct{ b *Builtins }

func (t *shellTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "run_shell",
		Description: "Run a shell command (via sh -c) in the working directory. Returns combined stdout+stderr and exit status. Use this to install tools, inspect files, run programs.",
		Parameters: map[string]domain.ParamSpec{
			"command": {Type: "string", Required: true, Description: "The shell command to execute."},
		},
	}
}

func (t *shellTool) Execute(ctx context.Context, args map[string]any) Result {
	cmd := str(args, "command")
	if strings.TrimSpace(cmd) == "" {
		return Result{IsError: true, Output: "command is empty"}
	}
	out, failed := t.b.runCmd(ctx, "sh", "-c", cmd)
	return Result{Output: out, IsError: failed}
}

// -----------------------------------------------------------------------------
// run_python
// -----------------------------------------------------------------------------

type pythonTool struct{ b *Builtins }

func (t *pythonTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "run_python",
		Description: "Run a Python 3 script in the working directory. The code is written to a file and executed; returns combined stdout+stderr. Install third-party packages FIRST with run_shell (pip install ...) before importing them.",
		Parameters: map[string]domain.ParamSpec{
			"code": {Type: "string", Required: true, Description: "The Python source to execute."},
		},
	}
}

func (t *pythonTool) Execute(ctx context.Context, args map[string]any) Result {
	code := str(args, "code")
	if strings.TrimSpace(code) == "" {
		return Result{IsError: true, Output: "code is empty"}
	}
	f := filepath.Join(t.b.Workdir, ".agent_step.py")
	if err := os.WriteFile(f, []byte(code), 0o644); err != nil {
		return Result{IsError: true, Output: "cannot write script: " + err.Error()}
	}
	out, failed := t.b.runCmd(ctx, t.b.PythonBin, f)
	return Result{Output: out, IsError: failed}
}

// -----------------------------------------------------------------------------
// read_file / write_file
// -----------------------------------------------------------------------------

type readFileTool struct{ b *Builtins }

func (t *readFileTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "read_file",
		Description: "Read a UTF-8 text file (relative paths resolve against the working directory).",
		Parameters: map[string]domain.ParamSpec{
			"path": {Type: "string", Required: true, Description: "File path to read."},
		},
	}
}

func (t *readFileTool) Execute(_ context.Context, args map[string]any) Result {
	p, err := t.b.resolveJailed(str(args, "path"))
	if err != nil {
		return Result{IsError: true, Output: err.Error()}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Result{IsError: true, Output: "read failed: " + err.Error()}
	}
	return Result{Output: clip(string(data))}
}

type writeFileTool struct{ b *Builtins }

func (t *writeFileTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "write_file",
		Description: "Write (create/overwrite) a UTF-8 text file (relative paths resolve against the working directory).",
		Parameters: map[string]domain.ParamSpec{
			"path":    {Type: "string", Required: true, Description: "File path to write."},
			"content": {Type: "string", Required: true, Description: "File contents."},
		},
	}
}

func (t *writeFileTool) Execute(_ context.Context, args map[string]any) Result {
	p, err := t.b.resolveJailed(str(args, "path"))
	if err != nil {
		return Result{IsError: true, Output: err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Result{IsError: true, Output: "mkdir failed: " + err.Error()}
	}
	if err := os.WriteFile(p, []byte(str(args, "content")), 0o644); err != nil {
		return Result{IsError: true, Output: "write failed: " + err.Error()}
	}
	return Result{Output: fmt.Sprintf("wrote %d bytes to %s", len(str(args, "content")), p)}
}

// -----------------------------------------------------------------------------
// edit_file — surgical, exact-string replacement (cheaper & safer than a full
// rewrite for large files).
// -----------------------------------------------------------------------------

type editFileTool struct{ b *Builtins }

func (t *editFileTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name: "edit_file",
		Description: "Replace an exact substring in an existing text file. By default old_string must occur EXACTLY ONCE " +
			"(include enough surrounding context to make it unique); set replace_all=true to replace every occurrence. " +
			"Prefer this over write_file for edits to large files. Use write_file to create a new file.",
		Parameters: map[string]domain.ParamSpec{
			"path":        {Type: "string", Required: true, Description: "File to edit (relative to the working directory)."},
			"old_string":  {Type: "string", Required: true, Description: "Exact text to find, including surrounding context for uniqueness."},
			"new_string":  {Type: "string", Required: true, Description: "Replacement text."},
			"replace_all": {Type: "bool", Required: false, Description: "Replace every occurrence instead of requiring a unique match."},
		},
	}
}

func (t *editFileTool) Execute(_ context.Context, args map[string]any) Result {
	p, err := t.b.resolveJailed(str(args, "path"))
	if err != nil {
		return Result{IsError: true, Output: err.Error()}
	}
	oldS := str(args, "old_string")
	newS := str(args, "new_string")
	replaceAll, _ := args["replace_all"].(bool)
	if oldS == "" {
		return Result{IsError: true, Output: "old_string is empty; use write_file to create or fully overwrite a file"}
	}
	if oldS == newS {
		return Result{IsError: true, Output: "old_string and new_string are identical; nothing to do"}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Result{IsError: true, Output: "read failed: " + err.Error()}
	}
	content := string(data)
	n := strings.Count(content, oldS)
	if n == 0 {
		return Result{IsError: true, Output: "old_string not found in " + p + " (it must match exactly, including whitespace)"}
	}
	if n > 1 && !replaceAll {
		return Result{IsError: true, Output: fmt.Sprintf("old_string is not unique: found %d occurrences in %s. Add more surrounding context, or set replace_all=true.", n, p)}
	}
	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldS, newS)
	} else {
		updated = strings.Replace(content, oldS, newS, 1)
	}
	if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
		return Result{IsError: true, Output: "write failed: " + err.Error()}
	}
	return Result{Output: fmt.Sprintf("edited %s (%d replacement(s))", p, n)}
}

// -----------------------------------------------------------------------------
// list_dir — structured directory listing (optionally recursive).
// -----------------------------------------------------------------------------

type listDirTool struct{ b *Builtins }

func (t *listDirTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name: "list_dir",
		Description: "List directory entries (relative paths resolve against the working directory). " +
			"Directories end with '/'. Set depth>1 to recurse. Use this to explore the file tree instead of shelling out to ls.",
		Parameters: map[string]domain.ParamSpec{
			"path":  {Type: "string", Required: false, Description: "Directory to list (default: working directory)."},
			"depth": {Type: "int", Required: false, Description: "Recursion depth (default 1, max 5)."},
		},
	}
}

func (t *listDirTool) Execute(_ context.Context, args map[string]any) Result {
	root, err := t.b.resolveJailed(strOr(args, "path", "."))
	if err != nil {
		return Result{IsError: true, Output: err.Error()}
	}
	depth := intArg(args, "depth", 1)
	if depth < 1 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}
	info, err := os.Stat(root)
	if err != nil {
		return Result{IsError: true, Output: "list failed: " + err.Error()}
	}
	if !info.IsDir() {
		return Result{IsError: true, Output: root + " is not a directory"}
	}

	const maxEntries = 500
	var lines []string
	truncated := false
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if isSkippedDir(d.Name()) {
				return fs.SkipDir
			}
			if strings.Count(rel, "/")+1 >= depth {
				lines = append(lines, rel+"/")
				return fs.SkipDir
			}
			lines = append(lines, rel+"/")
			return nil
		}
		size := int64(0)
		if fi, e := d.Info(); e == nil {
			size = fi.Size()
		}
		lines = append(lines, fmt.Sprintf("%s  (%s)", rel, humanSize(size)))
		if len(lines) >= maxEntries {
			truncated = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return Result{IsError: true, Output: "list failed: " + err.Error()}
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return Result{Output: "(empty directory)"}
	}
	out := strings.Join(lines, "\n")
	if truncated {
		out += fmt.Sprintf("\n...[truncated at %d entries]", maxEntries)
	}
	return Result{Output: clip(out)}
}

// -----------------------------------------------------------------------------
// glob — find files by name pattern (supports ** for any depth).
// -----------------------------------------------------------------------------

type globTool struct{ b *Builtins }

func (t *globTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name: "glob",
		Description: "Find files whose path matches a glob pattern. Supports '*' (within a path segment), '**' (any depth) " +
			"and '?'. Examples: '**/*.go', 'src/**/*.ts', '*.md'. Returns matching relative paths.",
		Parameters: map[string]domain.ParamSpec{
			"pattern": {Type: "string", Required: true, Description: "Glob pattern to match against relative paths."},
			"path":    {Type: "string", Required: false, Description: "Root directory to search under (default: working directory)."},
		},
	}
}

func (t *globTool) Execute(_ context.Context, args map[string]any) Result {
	pattern := strings.TrimSpace(str(args, "pattern"))
	if pattern == "" {
		return Result{IsError: true, Output: "pattern is empty"}
	}
	re, err := globToRegexp(pattern)
	if err != nil {
		return Result{IsError: true, Output: "bad pattern: " + err.Error()}
	}
	root, err := t.b.resolveJailed(strOr(args, "path", "."))
	if err != nil {
		return Result{IsError: true, Output: err.Error()}
	}
	baseOnly := !strings.Contains(pattern, "/")

	const maxMatches = 300
	var matches []string
	truncated := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && isSkippedDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if re.MatchString(rel) || (baseOnly && re.MatchString(path.Base(rel))) {
			matches = append(matches, rel)
			if len(matches) >= maxMatches {
				truncated = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if len(matches) == 0 {
		return Result{Output: "no files match " + pattern}
	}
	sort.Strings(matches)
	out := strings.Join(matches, "\n")
	if truncated {
		out += fmt.Sprintf("\n...[truncated at %d matches]", maxMatches)
	}
	return Result{Output: clip(out)}
}

// -----------------------------------------------------------------------------
// grep — search file contents by regex.
// -----------------------------------------------------------------------------

type grepTool struct{ b *Builtins }

func (t *grepTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name: "grep",
		Description: "Search file contents for a regular expression (Go/RE2 syntax) and return matching lines as " +
			"'path:line: text'. Optionally restrict to a subtree with 'path' and to filenames with an 'include' glob.",
		Parameters: map[string]domain.ParamSpec{
			"pattern":     {Type: "string", Required: true, Description: "Regular expression to search for."},
			"path":        {Type: "string", Required: false, Description: "File or directory to search under (default: working directory)."},
			"include":     {Type: "string", Required: false, Description: "Glob to filter filenames, e.g. '*.go' or '**/*.py'."},
			"ignore_case": {Type: "bool", Required: false, Description: "Case-insensitive match (default false)."},
		},
	}
}

func (t *grepTool) Execute(_ context.Context, args map[string]any) Result {
	pattern := str(args, "pattern")
	if strings.TrimSpace(pattern) == "" {
		return Result{IsError: true, Output: "pattern is empty"}
	}
	if ic, _ := args["ignore_case"].(bool); ic {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Result{IsError: true, Output: "bad regexp: " + err.Error()}
	}
	var incRe *regexp.Regexp
	if inc := strings.TrimSpace(str(args, "include")); inc != "" {
		if incRe, err = globToRegexp(inc); err != nil {
			return Result{IsError: true, Output: "bad include glob: " + err.Error()}
		}
	}
	root, err := t.b.resolveJailed(strOr(args, "path", "."))
	if err != nil {
		return Result{IsError: true, Output: err.Error()}
	}
	includeBaseOnly := incRe != nil && !strings.Contains(str(args, "include"), "/")

	const (
		maxHits    = 200
		maxPerFile = 20
		maxFileSz  = 2 << 20 // 2 MiB
	)
	var hits []string
	truncated := false

	visit := func(p string) {
		fi, e := os.Stat(p)
		if e != nil || fi.IsDir() || fi.Size() > maxFileSz {
			return
		}
		rel, _ := filepath.Rel(t.b.Workdir, p)
		rel = filepath.ToSlash(rel)
		if incRe != nil {
			relToRoot, _ := filepath.Rel(root, p)
			relToRoot = filepath.ToSlash(relToRoot)
			if !incRe.MatchString(relToRoot) && !(includeBaseOnly && incRe.MatchString(path.Base(p))) {
				return
			}
		}
		data, e := os.ReadFile(p)
		if e != nil || isBinary(data) {
			return
		}
		perFile := 0
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimRight(truncate(line, 300), "\r")))
				if perFile++; perFile >= maxPerFile {
					hits = append(hits, fmt.Sprintf("%s: ...[more matches in this file omitted]", rel))
					break
				}
				if len(hits) >= maxHits {
					truncated = true
					break
				}
			}
		}
	}

	info, err := os.Stat(root)
	if err != nil {
		return Result{IsError: true, Output: "grep failed: " + err.Error()}
	}
	if info.IsDir() {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return nil
			}
			if d.IsDir() {
				if p != root && isSkippedDir(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if len(hits) >= maxHits {
				truncated = true
				return fs.SkipAll
			}
			visit(p)
			return nil
		})
	} else {
		visit(root)
	}
	if len(hits) == 0 {
		return Result{Output: "no matches for /" + str(args, "pattern") + "/"}
	}
	out := strings.Join(hits, "\n")
	if truncated {
		out += fmt.Sprintf("\n...[truncated at %d matches]", maxHits)
	}
	return Result{Output: clip(out)}
}

// -----------------------------------------------------------------------------
// http_fetch
// -----------------------------------------------------------------------------

type httpFetchTool struct{ b *Builtins }

func (t *httpFetchTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "http_fetch",
		Description: "HTTP GET a URL and return the status code and response body (truncated). For anything more complex (headers, POST, cookies, JS) write code with run_python instead.",
		Parameters: map[string]domain.ParamSpec{
			"url": {Type: "string", Required: true, Description: "The URL to GET."},
		},
	}
}

func (t *httpFetchTool) Execute(ctx context.Context, args map[string]any) Result {
	url := str(args, "url")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{IsError: true, Output: "bad request: " + err.Error()}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; live-agent/2)")
	resp, err := t.b.http.Do(req)
	if err != nil {
		return Result{IsError: true, Output: "fetch failed: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxOutput*2))
	out := fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, clip(string(body)))
	return Result{Output: out, IsError: resp.StatusCode >= 400}
}

// -----------------------------------------------------------------------------
// reply
// -----------------------------------------------------------------------------

// replyTool is the "single-step return" the model chooses when the user's
// message needs no actions on the machine — a question, a greeting, a
// clarification. It ends the current turn with a direct answer and skips the
// task machinery (plan, verification). For anything requiring work on the
// host (files, commands, fetching), the model should use the action tools and
// finish instead.
type replyTool struct{}

func (t *replyTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name: "reply",
		Description: "Answer the user directly and end this turn WITHOUT doing any work on the machine. " +
			"Use this for greetings, questions, explanations, or clarifications — anything you can answer from knowledge or the conversation so far. " +
			"Do NOT use this if the request requires running commands, reading/writing files, or fetching data: use the action tools and finish for that.",
		Parameters: map[string]domain.ParamSpec{
			"text": {Type: "string", Required: true, Description: "The answer to show the user."},
		},
	}
}

func (t *replyTool) Execute(_ context.Context, args map[string]any) Result {
	text := str(args, "text")
	if strings.TrimSpace(text) == "" {
		return Result{IsError: true, Output: "text is empty"}
	}
	return Result{Reply: true, Output: text}
}

// -----------------------------------------------------------------------------
// set_title — name the conversation
// -----------------------------------------------------------------------------

type setTitleTool struct{}

func (t *setTitleTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name: "set_title",
		Description: "Give THIS conversation a short, human-friendly title (a few words, <=8) that summarises its topic. " +
			"Call this once, early, for a new conversation so it is easy to find later. This is metadata only — it does " +
			"not answer the user or complete the task, so still use reply or the action tools afterwards.",
		Parameters: map[string]domain.ParamSpec{
			"title": {Type: "string", Required: true, Description: "A concise title, ideally <=8 words, no surrounding quotes."},
		},
	}
}

func (t *setTitleTool) Execute(_ context.Context, args map[string]any) Result {
	title := strings.TrimSpace(str(args, "title"))
	title = strings.Trim(title, "\"'")
	if title == "" {
		return Result{IsError: true, Output: "title is empty"}
	}
	if len(title) > 120 {
		title = title[:120]
	}
	return Result{Title: title, Output: "title set: " + title}
}

type sendFileTool struct{ b *Builtins }

func (t *sendFileTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name: "send_file",
		Description: "Send a file from the working directory to the user so it shows up inline in the chat " +
			"(images are displayed; other files get a download link). Use this to deliver results such as " +
			"generated images, reports, or data files. The file must already exist in the working directory.",
		Parameters: map[string]domain.ParamSpec{
			"path":    {Type: "string", Required: true, Description: "Path of the file to send (relative to the working directory)."},
			"caption": {Type: "string", Required: false, Description: "Optional short caption to show with the file."},
		},
	}
}

func (t *sendFileTool) Execute(_ context.Context, args map[string]any) Result {
	raw := strings.TrimSpace(str(args, "path"))
	if raw == "" {
		return Result{IsError: true, Output: "path is empty"}
	}
	full := t.b.resolve(raw)
	info, err := os.Stat(full)
	if err != nil {
		return Result{IsError: true, Output: "file not found: " + raw}
	}
	if info.IsDir() {
		return Result{IsError: true, Output: raw + " is a directory, not a file"}
	}
	// Constrain to the working directory and report a workspace-relative path.
	rel, err := filepath.Rel(t.b.Workdir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return Result{IsError: true, Output: "file must be inside the working directory"}
	}
	rel = filepath.ToSlash(rel)
	caption := strings.TrimSpace(str(args, "caption"))
	return Result{SendFile: rel, Caption: caption, Output: "sent to user: " + rel}
}

// -----------------------------------------------------------------------------
// update_plan
// -----------------------------------------------------------------------------

type updatePlanTool struct{}

func (t *updatePlanTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "update_plan",
		Description: "Set or update your short TODO plan. Call this at the start and whenever the plan changes. Pass 'plan' as a JSON array of {\"step\": string, \"status\": \"pending\"|\"in_progress\"|\"done\"}.",
		Parameters: map[string]domain.ParamSpec{
			"plan": {Type: "any", Required: true, Description: "Array of {step, status} objects."},
		},
	}
}

func (t *updatePlanTool) Execute(_ context.Context, args map[string]any) Result {
	raw, err := coercePlanArray(args["plan"])
	if err != nil {
		return Result{IsError: true, Output: err.Error()}
	}
	var items []PlanItem
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		step, _ := m["step"].(string)
		status, _ := m["status"].(string)
		if step == "" {
			continue
		}
		if status == "" {
			status = "pending"
		}
		items = append(items, PlanItem{Step: step, Status: status})
	}
	if len(items) == 0 {
		return Result{IsError: true, Output: "no valid plan items"}
	}
	return Result{HasPlan: true, Plan: items, Output: fmt.Sprintf("plan updated (%d items)", len(items))}
}

// coercePlanArray accepts the plan either as a native JSON array (map decoding)
// or as a JSON-encoded string (many models stringify complex tool arguments).
func coercePlanArray(v any) ([]any, error) {
	switch p := v.(type) {
	case []any:
		return p, nil
	case string:
		var arr []any
		if err := json.Unmarshal([]byte(strings.TrimSpace(p)), &arr); err != nil {
			return nil, fmt.Errorf("plan string is not a JSON array: %v", err)
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("plan must be an array of {step, status} objects")
	}
}

// -----------------------------------------------------------------------------
// finish
// -----------------------------------------------------------------------------

type finishTool struct{}

func (t *finishTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "finish",
		Description: "End the run. Call this when the goal's success criteria are actually met, or when the goal is truly impossible. Provide a summary of what was accomplished (or why it failed).",
		Parameters: map[string]domain.ParamSpec{
			"summary": {Type: "string", Required: true, Description: "What was accomplished, or why the task cannot be completed."},
			"success": {Type: "bool", Required: false, Description: "true if the goal was achieved."},
		},
	}
}

func (t *finishTool) Execute(_ context.Context, args map[string]any) Result {
	success, _ := args["success"].(bool)
	return Result{Finished: true, Success: success, Output: str(args, "summary")}
}
