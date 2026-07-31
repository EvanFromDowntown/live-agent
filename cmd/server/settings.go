package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"liveagent/internal/domain"
	"liveagent/internal/llm"
)

// source is one OpenAI-compatible endpoint the UI can talk to: a base URL, its
// API key, and the models it offers. Multiple sources let you switch between
// providers/gateways and pick a model from any of them.
type source struct {
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key,omitempty"`
	Models  []string `json:"models"`
}

// embedSettings configures the (independent) embedding endpoint used for
// semantic lesson recall. It is separate from the chat sources because the chat
// gateway may not offer embeddings. An empty Model disables semantic recall
// (the agent then falls back to lexical matching).
type embedSettings struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key,omitempty"`
	Model   string `json:"model"`
}

// modelSettings is the runtime, UI-editable model configuration. It persists to
// ui-settings.json (0600, gitignored) INCLUDING API keys, so multiple sources
// remain usable across restarts; keys are never sent back to the browser.
type modelSettings struct {
	Sources []source      `json:"sources"`
	Model   string        `json:"model"`  // default model
	Source  string        `json:"source"` // base URL of the source the default model belongs to
	Embed   embedSettings `json:"embed"`  // independent embedding endpoint (optional)
	Vision  *bool         `json:"vision"` // send uploaded images to the model (nil = default on)
}

// visionOn reports whether uploaded images should be sent to the model.
func (m modelSettings) visionOn() bool { return m.Vision == nil || *m.Vision }

// initSettings seeds settings from env/config and overlays any persisted file.
func (s *srv) initSettings() {
	dbAbs := s.cfg.Agent.DBPath
	if !filepath.IsAbs(dbAbs) {
		if cwd, err := os.Getwd(); err == nil {
			dbAbs = filepath.Join(cwd, dbAbs)
		}
	}
	s.settingsPath = filepath.Join(filepath.Dir(dbAbs), "ui-settings.json")

	base := strings.TrimRight(os.Getenv("LLM_BASE_URL"), "/")
	key := os.Getenv("LLM_API_KEY")
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = s.cfg.LLM.Model
	}
	s.set = modelSettings{Model: model, Source: base}
	if base != "" {
		src := source{Name: "default", BaseURL: base, APIKey: key}
		if model != "" {
			src.Models = []string{model}
		}
		s.set.Sources = []source{src}
	}

	// Seed the embedding endpoint from its own env vars, falling back to the chat
	// connection for base/key so a single-gateway setup still works when it does
	// offer embeddings.
	embBase := strings.TrimRight(os.Getenv("LLM_EMBED_BASE_URL"), "/")
	if embBase == "" {
		embBase = base
	}
	embKey := os.Getenv("LLM_EMBED_API_KEY")
	if embKey == "" {
		embKey = key
	}
	embModel := os.Getenv("LLM_EMBED_MODEL")
	if embModel == "" {
		embModel = s.cfg.Learn.EmbedModel
	}
	s.set.Embed = embedSettings{BaseURL: embBase, APIKey: embKey, Model: embModel}

	if data, err := os.ReadFile(s.settingsPath); err == nil {
		var f modelSettings
		if json.Unmarshal(data, &f) == nil {
			if len(f.Sources) > 0 {
				// Preserve the env key for a matching base if the file omitted it.
				for i := range f.Sources {
					f.Sources[i].BaseURL = strings.TrimRight(f.Sources[i].BaseURL, "/")
					if f.Sources[i].APIKey == "" && f.Sources[i].BaseURL == base && key != "" {
						f.Sources[i].APIKey = key
					}
				}
				s.set.Sources = f.Sources
			}
			if f.Model != "" {
				s.set.Model = f.Model
			}
			if f.Source != "" {
				s.set.Source = strings.TrimRight(f.Source, "/")
			}
			if f.Embed.Model != "" || f.Embed.BaseURL != "" {
				fe := f.Embed
				fe.BaseURL = strings.TrimRight(fe.BaseURL, "/")
				if fe.APIKey == "" { // preserve env key when file omitted it
					fe.APIKey = s.set.Embed.APIKey
				}
				if fe.BaseURL == "" {
					fe.BaseURL = s.set.Embed.BaseURL
				}
				s.set.Embed = fe
			}
		}
	}
	s.normalizeDefault()
}

// normalizeDefault makes sure Source/Model point at a real source/model.
func (s *srv) normalizeDefault() {
	if len(s.set.Sources) == 0 {
		return
	}
	src := s.findSource(s.set.Source)
	if src == nil {
		src = &s.set.Sources[0]
		s.set.Source = src.BaseURL
	}
	found := false
	for _, m := range src.Models {
		if m == s.set.Model {
			found = true
			break
		}
	}
	if !found && len(src.Models) > 0 {
		s.set.Model = src.Models[0]
	}
}

// findSource returns the source with the given base URL (nil if none).
func (s *srv) findSource(base string) *source {
	base = strings.TrimRight(base, "/")
	for i := range s.set.Sources {
		if s.set.Sources[i].BaseURL == base {
			return &s.set.Sources[i]
		}
	}
	return nil
}

// persistSettings writes settings (including keys) to a 0600, gitignored file.
func (s *srv) persistSettings() {
	b, err := json.MarshalIndent(s.set, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.settingsPath, b, 0o600)
}

// buildLLM constructs a model client for the chosen source + model. Empty
// source/model fall back to the configured defaults, then to env.
func (s *srv) buildLLM(sourceBase, model string) (domain.LLM, error) {
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	if strings.TrimSpace(sourceBase) == "" {
		sourceBase = s.set.Source
	}
	var base, key string
	if src := s.findSource(sourceBase); src != nil {
		base, key = src.BaseURL, src.APIKey
	}
	if strings.TrimSpace(model) == "" {
		model = s.set.Model
	}
	return llm.NewModel(s.cfg.LLM, base, key, model)
}

// buildEmbedder constructs an embedder from the current embedding settings, or
// returns nil (semantic recall disabled) when no embedding model is configured.
func (s *srv) buildEmbedder() llm.Embedder {
	s.setMu.RLock()
	e := s.set.Embed
	s.setMu.RUnlock()
	emb := llm.NewEmbedderFrom(e.BaseURL, e.APIKey, e.Model, s.cfg.LLM.Timeout)
	if emb == nil { // avoid a non-nil interface wrapping a nil pointer
		return nil
	}
	return emb
}

// rebuildEmbedder recomputes the shared embedder (e.g. after a settings change).
// New sessions pick it up in resolveSession.
func (s *srv) rebuildEmbedder() {
	emb := s.buildEmbedder()
	s.setMu.Lock()
	s.embedder = emb
	s.setMu.Unlock()
	if emb != nil {
		s.logger.Info("embeddings.enabled", "model", s.set.Embed.Model)
	} else {
		s.logger.Info("embeddings.disabled")
	}
}

// currentEmbedder returns the shared embedder under lock (it may be swapped at
// runtime when settings change).
func (s *srv) currentEmbedder() llm.Embedder {
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	return s.embedder
}

// maskedSources copies sources with keys replaced by a has_key flag.
func (s *srv) maskedSources() []map[string]any {
	out := make([]map[string]any, 0, len(s.set.Sources))
	for _, src := range s.set.Sources {
		out = append(out, map[string]any{
			"name": src.Name, "base_url": src.BaseURL,
			"models": src.Models, "has_key": src.APIKey != "",
		})
	}
	return out
}

// handleSettings gets or updates the model sources. GET never returns API keys.
func (s *srv) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var in struct {
			Sources *[]source      `json:"sources"`
			Model   string         `json:"model"`
			Source  string         `json:"source"`
			Embed   *embedSettings `json:"embed"`
			Vision  *bool          `json:"vision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		s.setMu.Lock()
		if in.Sources != nil { // only replace the source list when provided
			next := make([]source, 0, len(*in.Sources))
			for _, src := range *in.Sources {
				src.BaseURL = strings.TrimRight(strings.TrimSpace(src.BaseURL), "/")
				if src.BaseURL == "" {
					continue
				}
				src.Name = strings.TrimSpace(src.Name)
				if src.Name == "" {
					src.Name = src.BaseURL
				}
				src.Models = cleanModels(src.Models)
				if strings.TrimSpace(src.APIKey) == "" { // keep existing key when blank
					if old := s.findSource(src.BaseURL); old != nil {
						src.APIKey = old.APIKey
					}
				} else {
					src.APIKey = strings.TrimSpace(src.APIKey)
				}
				next = append(next, src)
			}
			s.set.Sources = next
		}
		if strings.TrimSpace(in.Source) != "" {
			s.set.Source = strings.TrimRight(strings.TrimSpace(in.Source), "/")
		}
		if strings.TrimSpace(in.Model) != "" {
			s.set.Model = strings.TrimSpace(in.Model)
		}
		if in.Vision != nil {
			v := *in.Vision
			s.set.Vision = &v
		}
		if in.Embed != nil {
			e := *in.Embed
			e.BaseURL = strings.TrimRight(strings.TrimSpace(e.BaseURL), "/")
			e.Model = strings.TrimSpace(e.Model)
			if strings.TrimSpace(e.APIKey) == "" { // keep existing key when blank
				e.APIKey = s.set.Embed.APIKey
			} else {
				e.APIKey = strings.TrimSpace(e.APIKey)
			}
			s.set.Embed = e
		}
		s.normalizeDefault()
		s.persistSettings()
		s.setMu.Unlock()
		s.rebuildEmbedder() // reflect any embedding change for new sessions
	}
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	writeJSON(w, map[string]any{
		"sources": s.maskedSources(),
		"model":   s.set.Model,
		"source":  s.set.Source,
		"embed": map[string]any{
			"base_url": s.set.Embed.BaseURL,
			"model":    s.set.Embed.Model,
			"has_key":  s.set.Embed.APIKey != "",
		},
		"vision": s.set.visionOn(),
	})
}

// handleModels returns the sources (for the picker). With ?fetch=1&source=<base>
// it queries that source's /models endpoint, merges the ids in, and persists.
func (s *srv) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("fetch") == "1" {
		want := strings.TrimRight(strings.TrimSpace(r.URL.Query().Get("source")), "/")
		s.setMu.RLock()
		if want == "" {
			want = s.set.Source
		}
		var base, key string
		if src := s.findSource(want); src != nil {
			base, key = src.BaseURL, src.APIKey
		}
		s.setMu.RUnlock()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		ids, err := llm.FetchModels(ctx, base, key, 20*time.Second)
		if err != nil {
			writeJSON(w, map[string]any{"sources": s.maskedSources(), "error": err.Error()})
			return
		}
		s.setMu.Lock()
		if src := s.findSource(want); src != nil {
			for _, id := range ids {
				src.Models = mergeModels(src.Models, id)
			}
		}
		s.persistSettings()
		s.setMu.Unlock()
		s.setMu.RLock()
		defer s.setMu.RUnlock()
		writeJSON(w, map[string]any{"sources": s.maskedSources(), "fetched": ids, "source": want})
		return
	}
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	writeJSON(w, map[string]any{"sources": s.maskedSources(), "model": s.set.Model, "source": s.set.Source})
}

func cleanModels(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func mergeModels(list []string, m string) []string {
	m = strings.TrimSpace(m)
	if m == "" {
		return list
	}
	for _, e := range list {
		if e == m {
			return list
		}
	}
	return append(list, m)
}
