// Package reflection turns a finished episode into a structured, schema-validated
// reflection using the (frozen) LLM. It never mutates principles directly; it
// only emits candidates for the evolution package to process.
package reflection

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"liveagent/internal/domain"
	"liveagent/internal/llm"
)

// SuggestedSkill is the optional skill proposal inside a reflection.
type SuggestedSkill struct {
	Name  string             `json:"name"`
	Steps []domain.SkillStep `json:"steps"`
}

// Reflection is the strict JSON contract returned after every episode.
type Reflection struct {
	Success              bool            `json:"success"`
	Cause                string          `json:"cause"`
	Principle            string          `json:"principle"`
	ApplicableConditions []string        `json:"applicable_conditions"`
	Confidence           float64         `json:"confidence"`
	SuggestedSkill       *SuggestedSkill `json:"suggested_skill"`

	// Bookkeeping (not part of the model contract).
	Raw    string `json:"-"`
	Parsed bool   `json:"-"`
}

const systemPrompt = `You are the reflection module of a continually-learning agent.
Given the outcome of an episode, analyse success or failure and distil ONE reusable principle.
Respond with STRICT JSON only, matching this schema exactly:
{"success":bool,"cause":string,"principle":string,"applicable_conditions":[string],"confidence":number,"suggested_skill":{"name":string,"steps":[{"action":string,"parameters":object}]}|null}
Do not include any prose outside the JSON.`

// Reflector produces reflections via the LLM.
type Reflector struct {
	llm domain.LLM
}

// New builds a Reflector.
func New(model domain.LLM) *Reflector { return &Reflector{llm: model} }

// Reflect analyses an episode. On a parse failure it retries once; if it still
// cannot parse valid JSON it returns a Reflection with Parsed=false and the raw
// text preserved (the caller must NOT update principles in that case).
func (r *Reflector) Reflect(ctx context.Context, ep domain.Episode, body map[string]any) (Reflection, error) {
	user := llm.BuildUserMessage("Reflect on this episode and output the reflection JSON.", r.buildContext(ep, body))
	req := domain.LLMRequest{System: systemPrompt, User: user, Temperature: 0.2, MaxTokens: 512, JSONMode: true}

	var lastRaw string
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := r.llm.Generate(ctx, req)
		if err != nil {
			if attempt == 0 {
				continue
			}
			return Reflection{Raw: lastRaw, Parsed: false}, fmt.Errorf("reflection: llm error: %w", err)
		}
		lastRaw = resp.Text
		ref, perr := parse(resp.Text)
		if perr == nil {
			ref.Raw = resp.Text
			ref.Parsed = true
			return ref, nil
		}
	}
	// Retries exhausted: preserve raw output, do not fabricate a principle.
	return Reflection{Raw: lastRaw, Parsed: false}, nil
}

func (r *Reflector) buildContext(ep domain.Episode, body map[string]any) map[string]any {
	actions := make([]string, 0, len(ep.Trace))
	for _, e := range ep.Trace {
		actions = append(actions, e.Action.Name)
	}
	energyLow := false
	if e, ok := body["energy"].(float64); ok {
		energyLow = e < 30
	}
	return map[string]any{
		"task": "reflect",
		"episode": map[string]any{
			"goal":       ep.Goal,
			"success":    ep.Success,
			"actions":    actions,
			"reward":     ep.TotalReward,
			"energy_low": energyLow,
			"ticks":      ep.EndTick - ep.StartTick,
		},
		"body": body,
	}
}

// parse decodes and validates the reflection JSON contract.
func parse(text string) (Reflection, error) {
	obj, err := llm.ExtractJSONObject(text)
	if err != nil {
		return Reflection{}, err
	}
	var ref Reflection
	dec := json.NewDecoder(strings.NewReader(obj))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ref); err != nil {
		// Fall back to a lenient decode (models sometimes add extra keys).
		if err2 := json.Unmarshal([]byte(obj), &ref); err2 != nil {
			return Reflection{}, err2
		}
	}
	if err := validate(ref); err != nil {
		return Reflection{}, err
	}
	return ref, nil
}

// validate enforces the schema constraints beyond JSON shape.
func validate(r Reflection) error {
	if r.Principle == "" {
		return fmt.Errorf("reflection missing principle")
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return fmt.Errorf("confidence out of range: %v", r.Confidence)
	}
	if r.SuggestedSkill != nil {
		if r.SuggestedSkill.Name == "" {
			return fmt.Errorf("suggested_skill missing name")
		}
	}
	return nil
}
