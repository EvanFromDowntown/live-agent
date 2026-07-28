// Package safety enforces human-defined hard constraints. A hard constraint
// ALWAYS overrides the model's wish: a matching action is rejected before it is
// ever executed. Constraints are declarative (from config) and evaluated by
// built-in checkers only — never by executing arbitrary code.
package safety

import (
	"fmt"
	"strings"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

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
	if len(g.forbiddenShell) > 0 {
		payload := shellPayload(action)
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
