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

// modelSettings holds the runtime, UI-editable model connection. Base URL and
// model default from the environment (LLM_BASE_URL / LLM_MODEL) and can be
// overridden and persisted from the settings panel. The API key is kept in
// memory only (defaulting to LLM_API_KEY) and is never written to disk.
type modelSettings struct {
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"-"` // in-memory only; not persisted, not sent to the client
	Model   string   `json:"model"`
	Models  []string `json:"models"`
}

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
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = s.cfg.LLM.Model
	}
	s.set = modelSettings{
		BaseURL: base,
		APIKey:  os.Getenv("LLM_API_KEY"),
		Model:   model,
	}
	if model != "" {
		s.set.Models = []string{model}
	}

	// Overlay persisted, non-secret settings.
	if data, err := os.ReadFile(s.settingsPath); err == nil {
		var f modelSettings
		if json.Unmarshal(data, &f) == nil {
			if f.BaseURL != "" {
				s.set.BaseURL = strings.TrimRight(f.BaseURL, "/")
			}
			if f.Model != "" {
				s.set.Model = f.Model
			}
			if len(f.Models) > 0 {
				s.set.Models = f.Models
			}
		}
	}
	s.set.Models = mergeModels(s.set.Models, s.set.Model)
}

// persistSettings writes the non-secret settings to disk.
func (s *srv) persistSettings() {
	b, err := json.MarshalIndent(s.set, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.settingsPath, b, 0o600)
}

// buildLLM constructs a model client for the chosen model (empty = current
// default), using the runtime connection settings.
func (s *srv) buildLLM(model string) (domain.LLM, error) {
	s.setMu.RLock()
	base, key, def := s.set.BaseURL, s.set.APIKey, s.set.Model
	s.setMu.RUnlock()
	if strings.TrimSpace(model) == "" {
		model = def
	}
	return llm.NewModel(s.cfg.LLM, base, key, model)
}

// handleSettings gets or updates the model connection. GET never returns the
// API key; it reports has_key so the UI can show whether one is configured.
func (s *srv) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var in struct {
			BaseURL string   `json:"base_url"`
			APIKey  string   `json:"api_key"`
			Model   string   `json:"model"`
			Models  []string `json:"models"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		s.setMu.Lock()
		if strings.TrimSpace(in.BaseURL) != "" {
			s.set.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
		}
		if strings.TrimSpace(in.APIKey) != "" { // only replace when a new key is given
			s.set.APIKey = strings.TrimSpace(in.APIKey)
		}
		if len(in.Models) > 0 {
			s.set.Models = cleanModels(in.Models)
		}
		if strings.TrimSpace(in.Model) != "" {
			s.set.Model = strings.TrimSpace(in.Model)
			s.set.Models = mergeModels(s.set.Models, s.set.Model)
		}
		s.persistSettings()
		s.setMu.Unlock()
	}
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	writeJSON(w, map[string]any{
		"base_url": s.set.BaseURL,
		"model":    s.set.Model,
		"models":   s.set.Models,
		"has_key":  s.set.APIKey != "",
	})
}

// handleModels returns the model options. With ?fetch=1 it queries the
// provider's /models endpoint, merges the discovered ids into the saved list,
// and persists them.
func (s *srv) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("fetch") == "1" {
		s.setMu.RLock()
		base, key := s.set.BaseURL, s.set.APIKey
		s.setMu.RUnlock()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		ids, err := llm.FetchModels(ctx, base, key, 20*time.Second)
		if err != nil {
			writeJSON(w, map[string]any{"models": s.set.Models, "error": err.Error()})
			return
		}
		s.setMu.Lock()
		for _, id := range ids {
			s.set.Models = mergeModels(s.set.Models, id)
		}
		s.persistSettings()
		out := s.set.Models
		s.setMu.Unlock()
		writeJSON(w, map[string]any{"models": out, "fetched": ids})
		return
	}
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	writeJSON(w, map[string]any{"models": s.set.Models, "model": s.set.Model})
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
