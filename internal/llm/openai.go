package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
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
	apiPath               string // request path, default "/chat/completions"
	client                *http.Client
	streamClient          *http.Client // no overall timeout; streaming is bounded by context
	disableResponseFormat bool
	disableThinking       bool
	reasoningEffort       string
	omitTemperature       bool     // never send "temperature" (some models only accept their default)
	temperature           *float64 // force this temperature for every call (overrides the request's)
	maxTokens             int      // force this max_tokens for every call (0 = use the request's)
}

// OpenAIConfig configures the provider. BaseURL / APIKey / Model may be given
// explicitly (e.g. chosen in the UI); when empty they fall back to the env vars
// LLM_BASE_URL / LLM_API_KEY / LLM_MODEL. The remaining fields are per-endpoint
// knobs so different gateways/models can be tuned independently.
type OpenAIConfig struct {
	BaseURL               string
	APIKey                string
	Model                 string
	APIPath               string // override the request path (default "/chat/completions")
	Timeout               time.Duration
	DisableResponseFormat bool
	DisableThinking       bool
	ReasoningEffort       string
	OmitTemperature       bool     // drop the temperature field entirely
	Temperature           *float64 // pin temperature for this endpoint (nil = use per-call value)
	MaxTokens             int      // pin max_tokens for this endpoint (0 = use per-call value)
}

// NewOpenAIProvider builds a provider, preferring explicit config values and
// falling back to env vars LLM_BASE_URL / LLM_API_KEY / LLM_MODEL. It returns an
// error if required values are missing.
func NewOpenAIProvider(cfg OpenAIConfig) (*OpenAIProvider, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("LLM_BASE_URL"), "/")
	}
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv("LLM_API_KEY")
	}
	model := cfg.Model
	if model == "" {
		model = os.Getenv("LLM_MODEL")
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
	apiPath := strings.TrimSpace(cfg.APIPath)
	if apiPath == "" {
		apiPath = "/chat/completions"
	}
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	return &OpenAIProvider{
		baseURL:               base,
		apiKey:                key,
		model:                 model,
		apiPath:               apiPath,
		client:                &http.Client{Timeout: timeout},
		streamClient:          &http.Client{}, // bounded by ctx, not a fixed deadline
		disableResponseFormat: cfg.DisableResponseFormat,
		disableThinking:       cfg.DisableThinking,
		reasoningEffort:       cfg.ReasoningEffort,
		omitTemperature:       cfg.OmitTemperature,
		temperature:           cfg.Temperature,
		maxTokens:             cfg.MaxTokens,
	}, nil
}

// chatMessage.Content is either a plain string or a []contentPart (used for
// multimodal messages that carry images), so it is typed as any.
type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type contentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *imageURLPart `json:"image_url,omitempty"`
}

type imageURLPart struct {
	URL string `json:"url"`
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
	Temperature     *float64        `json:"temperature,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	Thinking        *thinkingParam  `json:"thinking,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	StreamOptions   *streamOptions  `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
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
			Role             string         `json:"role"`
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"` // GLM/DeepSeek-style thinking
			Reasoning        string         `json:"reasoning"`         // alternative field name
			ToolCalls        []toolCallWire `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage usageWire `json:"usage"`
}

type usageWire struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// buildRequest assembles the wire request shared by Generate and GenerateStream.
// forceOmitTemp drops the temperature field regardless of config; it is used to
// auto-recover from gateways that reject any explicit temperature for a model.
func (p *OpenAIProvider) buildRequest(req domain.LLMRequest, stream, forceOmitTemp bool) chatRequest {
	msgs := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if len(m.Images) > 0 {
			parts := make([]contentPart, 0, len(m.Images)+1)
			if strings.TrimSpace(m.Content) != "" {
				parts = append(parts, contentPart{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURLPart{URL: img}})
			}
			msgs = append(msgs, chatMessage{Role: m.Role, Content: parts})
			continue
		}
		msgs = append(msgs, chatMessage{Role: m.Role, Content: m.Content})
	}
	body := chatRequest{
		Model:     p.model,
		Messages:  msgs,
		MaxTokens: req.MaxTokens,
	}
	if p.maxTokens > 0 {
		body.MaxTokens = p.maxTokens
	}
	// Temperature is optional: omit it entirely when configured to (or when
	// forced by the auto-recovery path) so models that only accept their default
	// value don't 400. Otherwise a per-endpoint pin overrides the per-call value.
	if !p.omitTemperature && !forceOmitTemp {
		t := req.Temperature
		if p.temperature != nil {
			t = *p.temperature
		}
		body.Temperature = &t
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
	if stream {
		body.Stream = true
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return body
}

func (p *OpenAIProvider) newHTTPRequest(ctx context.Context, body chatRequest) (*http.Request, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+p.apiPath, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	return httpReq, nil
}

// Generate implements domain.LLM. If the gateway rejects the request solely
// because this model won't accept an explicit temperature, it retries once with
// the field omitted so the caller doesn't have to know each model's quirks.
func (p *OpenAIProvider) Generate(ctx context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	resp, err := p.generateOnce(ctx, req, false)
	if err != nil && !p.omitTemperature && isTemperatureError(err) {
		return p.generateOnce(ctx, req, true)
	}
	return resp, err
}

func (p *OpenAIProvider) generateOnce(ctx context.Context, req domain.LLMRequest, forceOmitTemp bool) (domain.LLMResponse, error) {
	httpReq, err := p.newHTTPRequest(ctx, p.buildRequest(req, false, forceOmitTemp))
	if err != nil {
		return domain.LLMResponse{}, err
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return domain.LLMResponse{}, fmt.Errorf("llm: request failed: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return domain.LLMResponse{}, &APIError{StatusCode: resp.StatusCode, Body: truncate(string(data), 300)}
	}
	var parsed chatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return domain.LLMResponse{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return domain.LLMResponse{}, fmt.Errorf("llm: empty choices")
	}
	msg := parsed.Choices[0].Message
	reasoning := msg.ReasoningContent
	if reasoning == "" {
		reasoning = msg.Reasoning
	}
	out := domain.LLMResponse{
		Text: msg.Content, Reasoning: reasoning,
		TokensUsed:       parsed.Usage.TotalTokens,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
	}
	for _, tc := range msg.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, domain.ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
		})
	}
	return out, nil
}

// streamChunk is one server-sent event during streaming.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *usageWire `json:"usage"`
}

// GenerateStream implements domain.StreamingLLM using OpenAI SSE streaming. A
// temperature rejection (a 400 before any token) is auto-recovered by retrying
// once with the field omitted.
func (p *OpenAIProvider) GenerateStream(ctx context.Context, req domain.LLMRequest, onDelta func(domain.StreamDelta)) (domain.LLMResponse, error) {
	resp, err := p.streamOnce(ctx, req, onDelta, false)
	if err != nil && !p.omitTemperature && isTemperatureError(err) {
		return p.streamOnce(ctx, req, onDelta, true)
	}
	return resp, err
}

func (p *OpenAIProvider) streamOnce(ctx context.Context, req domain.LLMRequest, onDelta func(domain.StreamDelta), forceOmitTemp bool) (domain.LLMResponse, error) {
	httpReq, err := p.newHTTPRequest(ctx, p.buildRequest(req, true, forceOmitTemp))
	if err != nil {
		return domain.LLMResponse{}, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := p.streamClient.Do(httpReq)
	if err != nil {
		return domain.LLMResponse{}, fmt.Errorf("llm: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return domain.LLMResponse{}, &APIError{StatusCode: resp.StatusCode, Body: truncate(string(data), 300)}
	}

	var content, reasoning strings.Builder
	type accTool struct {
		id, name string
		args     strings.Builder
	}
	tools := map[int]*accTool{}
	var order []int
	tokens, promptTokens, completionTokens := 0, 0, 0

	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(line[len("data:"):])
				if payload == "[DONE]" {
					break
				}
				var chunk streamChunk
				if json.Unmarshal([]byte(payload), &chunk) != nil {
					// tolerate partial/keepalive frames
				} else {
					if chunk.Usage != nil {
						if chunk.Usage.TotalTokens > 0 {
							tokens = chunk.Usage.TotalTokens
						}
						if chunk.Usage.PromptTokens > 0 {
							promptTokens = chunk.Usage.PromptTokens
						}
						if chunk.Usage.CompletionTokens > 0 {
							completionTokens = chunk.Usage.CompletionTokens
						}
					}
					for _, ch := range chunk.Choices {
						d := ch.Delta
						rz := d.ReasoningContent
						if rz == "" {
							rz = d.Reasoning
						}
						if rz != "" {
							reasoning.WriteString(rz)
							onDelta(domain.StreamDelta{Reasoning: rz})
						}
						if d.Content != "" {
							content.WriteString(d.Content)
							onDelta(domain.StreamDelta{Content: d.Content})
						}
						for _, tc := range d.ToolCalls {
							at := tools[tc.Index]
							if at == nil {
								at = &accTool{}
								tools[tc.Index] = at
								order = append(order, tc.Index)
							}
							if tc.ID != "" {
								at.id = tc.ID
							}
							if tc.Function.Name != "" {
								at.name = tc.Function.Name
							}
							if tc.Function.Arguments != "" {
								at.args.WriteString(tc.Function.Arguments)
							}
						}
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return domain.LLMResponse{}, fmt.Errorf("llm: stream read: %w", err)
		}
	}

	out := domain.LLMResponse{
		Text: content.String(), Reasoning: reasoning.String(),
		TokensUsed: tokens, PromptTokens: promptTokens, CompletionTokens: completionTokens,
	}
	sort.Ints(order)
	for _, idx := range order {
		at := tools[idx]
		if at.name == "" {
			continue
		}
		out.ToolCalls = append(out.ToolCalls, domain.ToolCall{ID: at.id, Name: at.name, Arguments: at.args.String()})
	}
	return out, nil
}

// FetchModels lists model ids from an OpenAI-compatible /models endpoint.
// Values fall back to the env vars when empty, mirroring provider construction.
func FetchModels(ctx context.Context, baseURL, apiKey string, timeout time.Duration) ([]string, error) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("LLM_BASE_URL"), "/")
	}
	key := apiKey
	if key == "" {
		key = os.Getenv("LLM_API_KEY")
	}
	if base == "" {
		return nil, fmt.Errorf("llm: base URL not set")
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: list models: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: models status %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("llm: decode models: %w", err)
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if strings.TrimSpace(m.ID) != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
