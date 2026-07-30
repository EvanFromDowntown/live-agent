package agent

import "liveagent/internal/tool"

// maxEventText bounds the reasoning/response text carried in an event so a very
// long think block cannot flood the stream.
const maxEventText = 8000

// Event is a structured progress signal emitted during a run so observers (e.g.
// a web UI) can render the loop in real time. It mirrors what is logged, but in
// a machine-friendly shape.
type Event struct {
	Type       string          `json:"type"` // start|step|plan|info|warn|verify|finish|stop
	EpisodeID  string          `json:"episode_id,omitempty"`
	Step       int             `json:"step,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Args       map[string]any  `json:"args,omitempty"`
	Output     string          `json:"output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	Plan       []tool.PlanItem `json:"plan,omitempty"`
	Success    bool            `json:"success,omitempty"`
	Verified   bool            `json:"verified,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	Text       string          `json:"text,omitempty"`
	Lessons    int             `json:"lessons,omitempty"`
	OS         string          `json:"os,omitempty"`
}

// Emitter receives run events. It must be safe to call from the run goroutine
// and should not block for long.
type Emitter func(Event)

func (a *Agent) emit(e Event) {
	if a.onEvent != nil {
		a.onEvent(e)
	}
}
