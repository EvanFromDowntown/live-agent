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
)

// Embedder turns text into vectors for semantic similarity. It is optional:
// when no embedding model is configured, the caller falls back to lexical
// matching. Credentials come from the same env vars as the chat provider.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// OpenAIEmbedder calls any OpenAI-compatible /embeddings endpoint.
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewEmbedder builds an Embedder from env (LLM_BASE_URL, LLM_API_KEY) and the
// given model (falls back to LLM_EMBED_MODEL). Returns (nil, nil) when no
// embedding model is configured — semantic recall is then simply disabled.
func NewEmbedder(model string, timeout time.Duration) (*OpenAIEmbedder, error) {
	if model == "" {
		model = os.Getenv("LLM_EMBED_MODEL")
	}
	if model == "" {
		return nil, nil // not configured; caller uses lexical fallback
	}
	base := strings.TrimRight(os.Getenv("LLM_BASE_URL"), "/")
	key := os.Getenv("LLM_API_KEY")
	if base == "" || key == "" {
		return nil, fmt.Errorf("llm: embeddings need LLM_BASE_URL and LLM_API_KEY")
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &OpenAIEmbedder{baseURL: base, apiKey: key, model: model, client: &http.Client{Timeout: timeout}}, nil
}

// NewEmbedderFrom builds an Embedder from explicit connection values, used by
// the web UI so the embedding endpoint can be configured independently of the
// chat gateway. Empty base/key fall back to the chat env vars. Returns nil (a
// true nil, safe for interface assignment) when no model is configured or the
// connection is incomplete — semantic recall is then simply disabled.
func NewEmbedderFrom(baseURL, apiKey, model string, timeout time.Duration) *OpenAIEmbedder {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("LLM_BASE_URL"), "/")
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		key = os.Getenv("LLM_API_KEY")
	}
	if base == "" || key == "" {
		return nil
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &OpenAIEmbedder{baseURL: base, apiKey: key, model: model, client: &http.Client{Timeout: timeout}}
}

// Model returns the configured embedding model name.
func (e *OpenAIEmbedder) Model() string { return e.model }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns one vector per input text, in input order.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, err
	}
	endpoint := e.baseURL
	if !strings.HasSuffix(endpoint, "/embeddings") {
		endpoint += "/embeddings"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: embeddings request failed: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: embeddings status %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var parsed embedResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("llm: decode embeddings: %w", err)
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	for i := range out {
		if out[i] == nil {
			return nil, fmt.Errorf("llm: embeddings missing vector for input %d", i)
		}
	}
	return out, nil
}
