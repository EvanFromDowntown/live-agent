package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"liveagent/internal/domain"
)

// decodeBody reads the JSON request body the provider sent.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, _ := io.ReadAll(r.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("bad request json: %v (%s)", err, raw)
	}
	return m
}

func TestBuildRequestTemperaturePolicy(t *testing.T) {
	pin := 0.7
	cases := []struct {
		name    string
		cfg     OpenAIConfig
		reqTemp float64
		force   bool
		want    any // nil means the field must be absent
	}{
		{"default sends per-call", OpenAIConfig{}, 0.2, false, 0.2},
		{"omit drops field", OpenAIConfig{OmitTemperature: true}, 0.2, false, nil},
		{"force drops field", OpenAIConfig{}, 0.2, true, nil},
		{"pin overrides per-call", OpenAIConfig{Temperature: &pin}, 0.2, false, 0.7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.BaseURL = "http://x"
			tc.cfg.APIKey = "k"
			tc.cfg.Model = "m"
			p, err := NewOpenAIProvider(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			body := p.buildRequest(domain.LLMRequest{Temperature: tc.reqTemp}, false, tc.force)
			if tc.want == nil {
				if body.Temperature != nil {
					t.Fatalf("expected temperature omitted, got %v", *body.Temperature)
				}
				return
			}
			if body.Temperature == nil {
				t.Fatalf("expected temperature %v, got nil", tc.want)
			}
			if *body.Temperature != tc.want.(float64) {
				t.Fatalf("temperature = %v, want %v", *body.Temperature, tc.want)
			}
		})
	}
}

// TestGenerateAutoRecoversTemperatureError verifies that a 400 blaming the
// temperature triggers exactly one retry with the field omitted, which then
// succeeds — without the caller knowing the model's quirk.
func TestGenerateAutoRecoversTemperatureError(t *testing.T) {
	var calls int
	var sawTempOnRetry *bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body := decodeBody(t, r)
		_, hasTemp := body["temperature"]
		if calls == 1 {
			if !hasTemp {
				t.Fatalf("first call should include temperature")
			}
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Unsupported value: 'temperature' does not support 0 with this model."}}`)
			return
		}
		v := hasTemp
		sawTempOnRetry = &v
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"total_tokens":5}}`)
	}))
	defer srv.Close()

	p, err := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Generate(context.Background(), domain.LLMRequest{Temperature: 0})
	if err != nil {
		t.Fatalf("expected auto-recovery, got error: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("unexpected content %q", resp.Text)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 calls (fail + retry), got %d", calls)
	}
	if sawTempOnRetry == nil || *sawTempOnRetry {
		t.Fatalf("retry must omit temperature")
	}
}

// TestGenerateDoesNotRetryUnrelated400 makes sure a 400 that is NOT about
// temperature is surfaced immediately (no wasted retry).
func TestGenerateDoesNotRetryUnrelated400(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"model not found"}}`)
	}))
	defer srv.Close()

	p, _ := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	_, err := p.Generate(context.Background(), domain.LLMRequest{Temperature: 0})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call, got %d", calls)
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAPIPathOverride(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"x"}}]}`)
	}))
	defer srv.Close()

	p, _ := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, APIKey: "k", Model: "m", APIPath: "v1/chat"})
	if _, err := p.Generate(context.Background(), domain.LLMRequest{}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chat" {
		t.Fatalf("path = %q, want /v1/chat", gotPath)
	}
}
