package safety

import (
	"testing"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

func TestForbiddenAction(t *testing.T) {
	g := NewGuard(config.SafetyConfig{ForbiddenActions: []string{"run_shell"}})
	if dec := g.Check(domain.Action{Name: "run_shell"}); dec.Allowed {
		t.Fatal("expected run_shell to be blocked")
	}
	if dec := g.Check(domain.Action{Name: "read_file"}); !dec.Allowed {
		t.Fatal("expected read_file to be allowed")
	}
}

func TestForbiddenShellPattern(t *testing.T) {
	g := NewGuard(config.SafetyConfig{ForbiddenShellPatterns: []string{"rm -rf /"}})
	danger := domain.Action{Name: "run_shell", Parameters: map[string]any{"command": "rm -rf / --no-preserve-root"}}
	if dec := g.Check(danger); dec.Allowed {
		t.Fatal("expected dangerous command to be blocked")
	}
	safe := domain.Action{Name: "run_shell", Parameters: map[string]any{"command": "ls -la"}}
	if dec := g.Check(safe); !dec.Allowed {
		t.Fatal("expected safe command to be allowed")
	}
}
