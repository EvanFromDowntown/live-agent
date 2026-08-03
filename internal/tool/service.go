package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"liveagent/internal/domain"
)

// serviceMetaDir is the per-workspace directory holding one JSON descriptor per
// background service. Persisting pid/meta to disk lets the web UI list and stop
// services even after a server restart, when the in-memory registry is gone.
const serviceMetaDir = ".services"

// ServiceInfo is a persisted background-service descriptor (also returned to the
// web UI). Alive is computed at read time from the pid, not stored.
type ServiceInfo struct {
	Name      string `json:"name"`
	Cmd       string `json:"cmd"`
	Port      int    `json:"port,omitempty"`
	PID       int    `json:"pid"`
	Log       string `json:"log"`
	StartedAt string `json:"started_at"`
	Alive     bool   `json:"alive"`
}

func metaPath(workdir, name string) string {
	return filepath.Join(workdir, serviceMetaDir, name+".json")
}

// writeServiceMeta persists a service descriptor so it can be recovered later.
func writeServiceMeta(workdir string, info ServiceInfo) {
	info.Alive = false // never persist the transient flag
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(metaPath(workdir, info.Name), data, 0o644)
}

func removeServiceMeta(workdir, name string) { _ = os.Remove(metaPath(workdir, name)) }

// ListServices reads all persisted service descriptors for a workspace and
// annotates each with whether its process is still alive. It is used by the web
// UI's services panel and is independent of any live tool registry.
func ListServices(workdir string) []ServiceInfo {
	dir := filepath.Join(workdir, serviceMetaDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []ServiceInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var info ServiceInfo
		if json.Unmarshal(data, &info) != nil || info.Name == "" {
			continue
		}
		info.Alive = alive(info.PID)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// StopService terminates a persisted service (whole process group) by name and
// removes its descriptor. Safe to call for an already-dead service.
func StopService(workdir, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name is empty")
	}
	data, err := os.ReadFile(metaPath(workdir, name))
	if err != nil {
		return "", fmt.Errorf("no such service: %s", name)
	}
	var info ServiceInfo
	if json.Unmarshal(data, &info) != nil {
		removeServiceMeta(workdir, name)
		return "", fmt.Errorf("corrupt service descriptor: %s", name)
	}
	pid := info.PID
	if pid <= 0 || !alive(pid) {
		removeServiceMeta(workdir, name)
		return fmt.Sprintf("service %q was not running; removed.", name), nil
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	for i := 0; i < 15 && alive(pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if alive(pid) {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		time.Sleep(150 * time.Millisecond)
	}
	removeServiceMeta(workdir, name)
	return fmt.Sprintf("stopped service %q (pid %d).", name, pid), nil
}

// bgService is a long-running process (e.g. an HTTP server) started detached so
// the agent's turn does NOT block on it. Output is redirected to a log file the
// agent can read; the process runs in its own group so stop kills its children.
type bgService struct {
	Name string
	Cmd  string
	Port int
	Log  string // workspace-relative log path
	cmd  *exec.Cmd

	mu      sync.Mutex
	exited  bool
	exitErr error
}

func (b *Builtins) registerService(s *bgService) string {
	b.svcMu.Lock()
	defer b.svcMu.Unlock()
	if b.services == nil {
		b.services = map[string]*bgService{}
	}
	name := s.Name
	for i := 1; ; i++ {
		if _, ok := b.services[name]; !ok {
			break
		}
		name = fmt.Sprintf("%s-%d", s.Name, i)
	}
	s.Name = name
	b.services[name] = s
	return name
}

// alive reports whether pid is still running (signal 0 probe).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// deriveName picks a short service name from a command line.
func deriveName(command string) string {
	f := strings.Fields(command)
	for _, tok := range f {
		if strings.Contains(tok, "=") || strings.HasPrefix(tok, "-") {
			continue
		}
		base := filepath.Base(tok)
		if base != "" && base != "sudo" && base != "nohup" && base != "env" {
			return strings.TrimSuffix(base, filepath.Ext(base))
		}
	}
	return "service"
}

// -----------------------------------------------------------------------------
// start_service
// -----------------------------------------------------------------------------

type startServiceTool struct{ b *Builtins }

func (t *startServiceTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name: "start_service",
		Description: "Start a LONG-RUNNING / blocking command (e.g. an HTTP server like 'python3 -m http.server 8000', " +
			"a dev server, or a watcher) as a detached background process, so this turn does NOT hang waiting on it. " +
			"Returns immediately with the pid and a log file path. Use this instead of run_shell for anything that does " +
			"not exit on its own. After starting, verify with http_fetch, read the log with read_file, and stop it with " +
			"stop_service.",
		Parameters: map[string]domain.ParamSpec{
			"command": {Type: "string", Required: true, Description: "The command to run (executed via /bin/sh -c in the working directory). Include the port in the command itself, e.g. 'python3 -m http.server 8000'."},
			"name":    {Type: "string", Required: false, Description: "Optional short name to reference the service later."},
		},
	}
}

func (t *startServiceTool) Execute(_ context.Context, args map[string]any) Result {
	command := strings.TrimSpace(str(args, "command"))
	if command == "" {
		return Result{IsError: true, Output: "command is empty"}
	}
	name := strings.TrimSpace(str(args, "name"))
	if name == "" {
		name = deriveName(command)
	}
	port := detectPort(command)

	dir := filepath.Join(t.b.Workdir, ".services")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{IsError: true, Output: "cannot create .services dir: " + err.Error()}
	}
	logRel := filepath.Join(".services", name+".log")
	logPath := filepath.Join(t.b.Workdir, logRel)
	logf, err := os.Create(logPath)
	if err != nil {
		return Result{IsError: true, Output: "cannot open log file: " + err.Error()}
	}

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = t.b.Workdir
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own process group so stop kills children too
	if err := cmd.Start(); err != nil {
		logf.Close()
		return Result{IsError: true, Output: "failed to start: " + err.Error()}
	}

	svc := &bgService{Name: name, Cmd: command, Port: port, Log: logRel, cmd: cmd}
	// Reap the process when it eventually exits (avoids zombies) and record why.
	go func() {
		werr := cmd.Wait()
		logf.Close()
		svc.mu.Lock()
		svc.exited = true
		svc.exitErr = werr
		svc.mu.Unlock()
	}()

	name = t.b.registerService(svc)
	pid := cmd.Process.Pid
	writeServiceMeta(t.b.Workdir, ServiceInfo{
		Name: name, Cmd: command, Port: port, PID: pid, Log: logRel,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	})

	// Give it a moment; if it died immediately (e.g. port in use), surface the log.
	time.Sleep(600 * time.Millisecond)
	svc.mu.Lock()
	exited, exitErr := svc.exited, svc.exitErr
	svc.mu.Unlock()
	if exited {
		tail := readLogTail(logPath, 1500)
		msg := fmt.Sprintf("service %q exited immediately", name)
		if exitErr != nil {
			msg += " (" + exitErr.Error() + ")"
		}
		if tail != "" {
			msg += "\n--- log ---\n" + tail
		}
		return Result{IsError: true, Output: msg}
	}

	out := fmt.Sprintf("started service %q (pid %d), running in background.", name, pid)
	if port > 0 {
		out += fmt.Sprintf(" Listening port: %d (try http_fetch http://localhost:%d/).", port, port)
	}
	out += fmt.Sprintf(" Logs: %s (read with read_file). Stop with stop_service name=%q.", svc.Log, name)
	return Result{Output: out}
}

// -----------------------------------------------------------------------------
// list_services
// -----------------------------------------------------------------------------

type listServicesTool struct{ b *Builtins }

func (t *listServicesTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "list_services",
		Description: "List background services started this session (name, pid, port, alive/exited, log path).",
		Parameters:  map[string]domain.ParamSpec{},
	}
}

func (t *listServicesTool) Execute(_ context.Context, _ map[string]any) Result {
	t.b.svcMu.Lock()
	defer t.b.svcMu.Unlock()
	if len(t.b.services) == 0 {
		return Result{Output: "no background services running."}
	}
	var b strings.Builder
	for name, s := range t.b.services {
		pid := 0
		if s.cmd != nil && s.cmd.Process != nil {
			pid = s.cmd.Process.Pid
		}
		state := "running"
		s.mu.Lock()
		if s.exited {
			state = "exited"
		}
		s.mu.Unlock()
		if state == "running" && !alive(pid) {
			state = "exited"
		}
		fmt.Fprintf(&b, "- %s: pid=%d state=%s", name, pid, state)
		if s.Port > 0 {
			fmt.Fprintf(&b, " port=%d", s.Port)
		}
		fmt.Fprintf(&b, " log=%s cmd=%q\n", s.Log, s.Cmd)
	}
	return Result{Output: strings.TrimRight(b.String(), "\n")}
}

// -----------------------------------------------------------------------------
// stop_service
// -----------------------------------------------------------------------------

type stopServiceTool struct{ b *Builtins }

func (t *stopServiceTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "stop_service",
		Description: "Stop a background service started with start_service (by name). Terminates the whole process group.",
		Parameters: map[string]domain.ParamSpec{
			"name": {Type: "string", Required: true, Description: "The service name returned by start_service / list_services."},
		},
	}
}

func (t *stopServiceTool) Execute(_ context.Context, args map[string]any) Result {
	name := strings.TrimSpace(str(args, "name"))
	if name == "" {
		return Result{IsError: true, Output: "name is empty"}
	}
	t.b.svcMu.Lock()
	svc := t.b.services[name]
	t.b.svcMu.Unlock()
	if svc == nil {
		return Result{IsError: true, Output: "no such service: " + name}
	}
	pid := 0
	if svc.cmd != nil && svc.cmd.Process != nil {
		pid = svc.cmd.Process.Pid
	}
	if pid <= 0 || !alive(pid) {
		t.b.svcMu.Lock()
		delete(t.b.services, name)
		t.b.svcMu.Unlock()
		removeServiceMeta(t.b.Workdir, name)
		return Result{Output: fmt.Sprintf("service %q was not running; removed.", name)}
	}
	// Signal the whole group (negative pid). SIGTERM first, then SIGKILL.
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	for i := 0; i < 15 && alive(pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if alive(pid) {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		time.Sleep(150 * time.Millisecond)
	}
	t.b.svcMu.Lock()
	delete(t.b.services, name)
	t.b.svcMu.Unlock()
	removeServiceMeta(t.b.Workdir, name)
	return Result{Output: fmt.Sprintf("stopped service %q (pid %d).", name, pid)}
}

var portRe = regexp.MustCompile(`(?:--port[ =]|-p[ =]|:)?(\b\d{2,5}\b)`)

// detectPort makes a best-effort guess at the port a service command binds,
// used only for a friendlier message / verification hint.
func detectPort(command string) int {
	// Prefer explicit --port/-p/:port forms, else the last 2-5 digit number.
	best := 0
	for _, m := range portRe.FindAllStringSubmatch(command, -1) {
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		if n >= 1 && n <= 65535 {
			best = n
		}
	}
	return best
}

// readLogTail returns up to n trailing bytes of a log file.
func readLogTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > n {
		data = data[len(data)-n:]
	}
	return strings.TrimSpace(string(data))
}
