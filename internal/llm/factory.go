package llm

import (
	"context"
	"fmt"
	"time"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

// New builds a domain.LLM from configuration, wrapping it with retry/timeout.
// Only the OpenAI-compatible provider is supported; it reads credentials from
// environment variables (LLM_BASE_URL / LLM_API_KEY / LLM_MODEL).
func New(cfg config.LLMConfig) (domain.LLM, error) {
	return NewModel(cfg, "", "", "")
}

// NewModel is like New but takes explicit connection overrides (base URL, API
// key, model) — used by the web UI so a session can pick its model and endpoint
// at runtime. Empty overrides fall back to config / env.
func NewModel(cfg config.LLMConfig, baseURL, apiKey, model string) (domain.LLM, error) {
	switch cfg.Provider {
	case "openai", "":
		if model == "" {
			model = cfg.Model
		}
		p, err := NewOpenAIProvider(OpenAIConfig{
			BaseURL:               baseURL,
			APIKey:                apiKey,
			Model:                 model,
			Timeout:               cfg.Timeout,
			DisableResponseFormat: cfg.DisableResponseFormat,
			DisableThinking:       cfg.DisableThinking,
			ReasoningEffort:       cfg.ReasoningEffort,
		})
		if err != nil {
			return nil, err
		}
		return &Retrying{inner: p, retries: cfg.MaxRetries, timeout: cfg.Timeout}, nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
}

// Retrying wraps a domain.LLM with a per-call timeout and bounded retries.
type Retrying struct {
	inner   domain.LLM
	retries int
	timeout time.Duration
}

// Generate implements domain.LLM.
func (r *Retrying) Generate(ctx context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	attempts := r.retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		callCtx := ctx
		var cancel context.CancelFunc
		if r.timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, r.timeout)
		}
		resp, err := r.inner.Generate(callCtx, req)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return domain.LLMResponse{}, fmt.Errorf("llm: generate failed after %d attempts: %w", attempts, lastErr)
}

// GenerateStream implements domain.StreamingLLM. It delegates to a streaming
// inner provider (bounded by the caller's context, not a fixed per-call
// deadline, so long streams are not truncated). If the inner provider does not
// support streaming it falls back to a single blocking Generate. Retries stop
// once any token has been emitted, to avoid showing duplicated partial output.
func (r *Retrying) GenerateStream(ctx context.Context, req domain.LLMRequest, onDelta func(domain.StreamDelta)) (domain.LLMResponse, error) {
	s, ok := r.inner.(domain.StreamingLLM)
	if !ok {
		return r.Generate(ctx, req)
	}
	attempts := r.retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		emitted := false
		wrapped := func(d domain.StreamDelta) {
			emitted = true
			onDelta(d)
		}
		resp, err := s.GenerateStream(ctx, req, wrapped)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil || emitted {
			break
		}
	}
	return domain.LLMResponse{}, fmt.Errorf("llm: stream failed after %d attempts: %w", attempts, lastErr)
}
