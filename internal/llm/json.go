package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ContextSentinel marks the start of the machine-readable JSON context block
// that cognition appends to the user message. The FakeLLM parses it; a real LLM
// simply reads it as additional (well-structured) context.
const ContextSentinel = "@@CTX@@"

// ExtractContext pulls the JSON object that follows ContextSentinel out of a
// user message and decodes it into a generic map. Returns nil if absent.
func ExtractContext(user string) map[string]any {
	idx := strings.Index(user, ContextSentinel)
	if idx < 0 {
		return nil
	}
	rest := user[idx+len(ContextSentinel):]
	obj, err := extractFirstJSONObject(rest)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(obj), &m); err != nil {
		return nil
	}
	return m
}

// ExtractJSONObject finds the first balanced JSON object in a string, tolerating
// markdown code fences and surrounding prose. This is how we defensively parse
// real-LLM output that may not be pure JSON.
func ExtractJSONObject(s string) (string, error) {
	s = stripCodeFences(s)
	return extractFirstJSONObject(s)
}

func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// drop opening fence line (```json or ```)
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		if end := strings.LastIndex(s, "```"); end >= 0 {
			s = s[:end]
		}
	}
	return s
}

func extractFirstJSONObject(s string) (string, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", fmt.Errorf("no JSON object found")
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("unbalanced JSON object")
}
