package llm

import (
	"context"
	"fmt"
	"time"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

// New builds a domain.LLM from configuration, wrapping it with retry/timeout.
// Provider "fake" needs no credentials; "openai" reads them from env vars.
func New(cfg config.LLMConfig) (domain.LLM, error) {
	var base domain.LLM
	switch cfg.Provider {
	case "fake":
		base = NewFakeLLM()
	case "openai":
		p, err := NewOpenAIProvider(OpenAIConfig{Model: cfg.Model, Timeout: cfg.Timeout})
		if err != nil {
			return nil, err
		}
		base = p
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
	return &Retrying{inner: base, retries: cfg.MaxRetries, timeout: cfg.Timeout}, nil
}

// Retrying wraps a domain.LLM with a per-call timeout and bounded retries. This
// is the "error degradation" layer: after exhausting retries it returns the last
// error and the caller falls back to a heuristic.
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
