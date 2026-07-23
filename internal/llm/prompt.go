package llm

import "encoding/json"

// BuildUserMessage assembles a user message from a human-readable instruction
// plus a machine-readable context block delimited by ContextSentinel. Real LLMs
// read both; the FakeLLM parses the context block. The context map MUST contain
// a "task" key so providers can branch on it.
func BuildUserMessage(instruction string, ctx map[string]any) string {
	b, _ := json.Marshal(ctx)
	return instruction + "\n" + ContextSentinel + "\n" + string(b)
}
