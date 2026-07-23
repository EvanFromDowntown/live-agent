package webagent

import "testing"

func TestSubstantive(t *testing.T) {
	// Require a non-empty "content" field of >= 20 chars (the zhihu setting).
	e := &Env{successFields: []string{"content"}, minFieldChars: 20}

	realAnswer := "这是一个足够长的真实回答内容，包含实际信息。" // > 20 runes
	cases := []struct {
		name string
		rec  any
		want bool
	}{
		{"placeholder token", map[string]any{"content": "NO_ANSWERS_FOUND"}, false},
		{"empty content", map[string]any{"content": ""}, false},
		{"too short", map[string]any{"content": "太短了"}, false},
		{"missing field", map[string]any{"author": "someone"}, false},
		{"real answer", map[string]any{"author": "作者", "content": realAnswer}, true},
	}
	for _, c := range cases {
		if got := e.substantive(c.rec); got != c.want {
			t.Errorf("%s: substantive=%v want %v", c.name, got, c.want)
		}
	}
}

func TestSubstantiveNoFields(t *testing.T) {
	// Without configured fields: at least one non-placeholder value of min length.
	e := &Env{minFieldChars: 1}
	if e.substantive(map[string]any{"text": "", "author": ""}) {
		t.Error("all-empty object should not be substantive")
	}
	if !e.substantive(map[string]any{"text": "hi", "author": "x"}) {
		t.Error("object with real values should be substantive")
	}
	if e.substantive(map[string]any{"x": "none"}) {
		t.Error("placeholder-only object should not be substantive")
	}
}
