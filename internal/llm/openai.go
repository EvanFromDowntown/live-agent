package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"liveagent/internal/domain"
)

// OpenAIProvider talks to any OpenAI-compatible /chat/completions endpoint.
// Credentials are read from environment variables ONLY — never from config or
// source — so no secret is ever committed.
type OpenAIProvider struct {
	baseURL               string
	apiKey                string
	model                 string
	client                *http.Client
	disableResponseFormat bool
	disableThinking       bool
	reasoningEffort       string
}

// OpenAIConfig configures the provider. Model may be overridden by LLM_MODEL.
type OpenAIConfig struct {
	Model                 string
	Timeout               time.Duration
	DisableResponseFormat bool
	DisableThinking       bool
	ReasoningEffort       string
}

// NewOpenAIProvider builds a provider from env vars LLM_BASE_URL, LLM_API_KEY,
// LLM_MODEL. It returns an error if required values are missing.
func NewOpenAIProvider(cfg OpenAIConfig) (*OpenAIProvider, error) {
	base := strings.TrimRight(os.Getenv("LLM_BASE_URL"), "/")
	key := os.Getenv("LLM_API_KEY")
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = cfg.Model
	}
	if base == "" {
		return nil, fmt.Errorf("llm: LLM_BASE_URL is not set")
	}
	if key == "" {
		return nil, fmt.Errorf("llm: LLM_API_KEY is not set")
	}
	if model == "" {
		return nil, fmt.Errorf("llm: model not set (LLM_MODEL or config.llm.model)")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &OpenAIProvider{
		baseURL:               base,
		apiKey:                key,
		model:                 model,
		client:                &http.Client{Timeout: timeout},
		disableResponseFormat: cfg.DisableResponseFormat,
		disableThinking:       cfg.DisableThinking,
		reasoningEffort:       cfg.ReasoningEffort,
	}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type toolFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type toolDefWire struct {
	Type     string          `json:"type"`
	Function toolFunctionDef `json:"function"`
}

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	Tools           []toolDefWire   `json:"tools,omitempty"`
	ToolChoice      string          `json:"tool_choice,omitempty"`
	Temperature     float64         `json:"temperature"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	Thinking        *thinkingParam  `json:"thinking,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type thinkingParam struct {
	Type string `json:"type"`
}

type toolCallWire struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role      string         `json:"role"`
			Content   string         `json:"content"`
			ToolCalls []toolCallWire `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Generate implements domain.LLM.
func (p *OpenAIProvider) Generate(ctx context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	msgs := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, chatMessage{Role: m.Role, Content: m.Content})
	}
	body := chatRequest{
		Model:       p.model,
		Messages:    msgs,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]toolDefWire, 0, len(req.Tools))
		for _, t := range req.Tools {
			body.Tools = append(body.Tools, toolDefWire{
				Type:     "function",
				Function: toolFunctionDef{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
			})
		}
		if req.ToolChoice != "" {
			body.ToolChoice = req.ToolChoice
		}
	}
	// response_format(JSON mode) and tool-calling are mutually exclusive on many
	// gateways; only request JSON mode when NOT using tools.
	if req.JSONMode && !p.disableResponseFormat && len(req.Tools) == 0 {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	if p.disableThinking {
		body.Thinking = &thinkingParam{Type: "disabled"}
	}
	if p.reasoningEffort != "" {
		body.ReasoningEffort = p.reasoningEffort
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return domain.LLMResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return domain.LLMResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return domain.LLMResponse{}, fmt.Errorf("llm: request failed: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return domain.LLMResponse{}, fmt.Errorf("llm: status %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var parsed chatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return domain.LLMResponse{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return domain.LLMResponse{}, fmt.Errorf("llm: empty choices")
	}
	msg := parsed.Choices[0].Message
	out := domain.LLMResponse{Text: msg.Content, TokensUsed: parsed.Usage.TotalTokens}
	for _, tc := range msg.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, domain.ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
		})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
