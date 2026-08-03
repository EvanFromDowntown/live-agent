package llm

import (
	"errors"
	"fmt"
	"testing"
)

func TestAPIErrorRetryable(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{400, false}, // bad request — permanent
		{401, false}, // unauthorized — permanent
		{404, false}, // not found — permanent
		{422, false}, // unprocessable — permanent
		{429, true},  // rate limited — retry
		{500, true},  // server error — retry
		{503, true},  // unavailable — retry
	}
	for _, c := range cases {
		e := &APIError{StatusCode: c.code}
		if got := e.Retryable(); got != c.want {
			t.Errorf("status %d: Retryable()=%v, want %v", c.code, got, c.want)
		}
		// The package-level helper must agree, including when wrapped.
		wrapped := fmt.Errorf("llm: generate failed: %w", e)
		if got := retryable(wrapped); got != c.want {
			t.Errorf("status %d wrapped: retryable()=%v, want %v", c.code, got, c.want)
		}
	}
}

func TestRetryableNonAPIError(t *testing.T) {
	// A plain transport error (no HTTP status) is treated as transient.
	if !retryable(errors.New("connection reset by peer")) {
		t.Fatal("expected non-APIError to be retryable")
	}
}
