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
}

// OpenAIConfig configures the provider. Model may be overridden by LLM_MODEL.
type OpenAIConfig struct {
	Model                 string
	Timeout               time.Duration
	DisableResponseFormat bool
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
	}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Generate implements domain.LLM.
func (p *OpenAIProvider) Generate(ctx context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	body := chatRequest{
		Model: p.model,
		Messages: []chatMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if req.JSONMode && !p.disableResponseFormat {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
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
	return domain.LLMResponse{
		Text:       parsed.Choices[0].Message.Content,
		TokensUsed: parsed.Usage.TotalTokens,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
