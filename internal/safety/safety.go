// Package safety enforces human-defined hard constraints. A hard constraint
// ALWAYS overrides the model's wish: a matching action is rejected before it is
// ever executed. Constraints are declarative (from config) and evaluated by
// built-in checkers only — never by executing arbitrary code.
package safety

import (
	"fmt"
	"regexp"
	"strings"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

// builtinDanger is a set of always-on hard blocks for catastrophic shell/python
// payloads, independent of (and in addition to) config-driven patterns. These
// are deliberately conservative: they target unambiguously destructive or
// host-compromising operations, not merely risky ones.
var builtinDanger = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z]*\s+)*-[a-zA-Z]*[rR][a-zA-Z]*f|rm\s+-[a-zA-Z]*f[a-zA-Z]*[rR]`), "recursive force-delete (rm -rf)"},
	{regexp.MustCompile(`\brm\s+-[a-zA-Z]*[rRf].*\s(/|~|\$HOME|/\*)(\s|$)`), "recursive delete of a root/home path"},
	{regexp.MustCompile(`:\s*\(\)\s*\{.*\}\s*;?\s*:`), "fork bomb"},
	{regexp.MustCompile(`\bmkfs(\.\w+)?\b`), "filesystem format (mkfs)"},
	{regexp.MustCompile(`\bdd\b.*\bof=/dev/`), "raw write to a block device (dd of=/dev/...)"},
	{regexp.MustCompile(`>\s*/dev/(sd|nvme|disk|hd)`), "redirect to a raw disk device"},
	{regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff)\b`), "power/shutdown command"},
	{regexp.MustCompile(`\b(chmod|chown)\s+-[a-zA-Z]*R[a-zA-Z]*\s+.*\s(/|/etc|/usr|/bin)(\s|$)`), "recursive permission change on a system path"},
	{regexp.MustCompile(`\b(curl|wget)\b[^\n|]*\|\s*(sudo\s+)?(sh|bash|zsh)\b`), "pipe-to-shell of remote content (curl|wget ... | sh)"},
	{regexp.MustCompile(`>\s*/etc/`), "overwrite of a system config file under /etc"},
}

// Decision is the result of a safety check.
type Decision struct {
	Allowed bool
	Reason  string
}

// Guard evaluates actions against configured hard constraints.
type Guard struct {
	forbiddenActions map[string]bool
	forbiddenShell   []string
}

// NewGuard builds a Guard from config.
func NewGuard(cfg config.SafetyConfig) *Guard {
	fa := make(map[string]bool, len(cfg.ForbiddenActions))
	for _, a := range cfg.ForbiddenActions {
		fa[a] = true
	}
	return &Guard{forbiddenActions: fa, forbiddenShell: cfg.ForbiddenShellPatterns}
}

// Check returns a Decision for the given action. It is side-effect free so it
// can be called during planning and again immediately before execution.
func (g *Guard) Check(action domain.Action) Decision {
	if g.forbiddenActions[action.Name] {
		return Decision{Allowed: false, Reason: fmt.Sprintf("action %q is forbidden by policy", action.Name)}
	}
	payload := shellPayload(action)
	if payload != "" {
		for _, d := range builtinDanger {
			if d.re.MatchString(payload) {
				return Decision{Allowed: false, Reason: fmt.Sprintf("blocked as dangerous: %s", d.reason)}
			}
		}
		for _, pat := range g.forbiddenShell {
			if pat != "" && strings.Contains(payload, pat) {
				return Decision{Allowed: false, Reason: fmt.Sprintf("payload matches forbidden pattern %q", pat)}
			}
		}
	}
	return Decision{Allowed: true}
}

// shellPayload extracts the executable text from a code-running action so it can
// be pattern-checked.
func shellPayload(a domain.Action) string {
	var b strings.Builder
	for _, k := range []string{"script", "code", "command"} {
		if v, ok := a.Parameters[k].(string); ok {
			b.WriteString(v)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
