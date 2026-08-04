package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is a typed HTTP-level failure from the model endpoint. It carries the
// status code so retry logic can distinguish permanent client errors (4xx,
// which will never succeed on retry) from transient ones (429 rate limits, 5xx
// server errors, network faults).
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm: status %d: %s", e.StatusCode, e.Body)
}

// Retryable reports whether re-issuing the same request could plausibly
// succeed. 4xx client errors (bad request, auth, not found, …) are permanent —
// except 429 (rate limited), which is worth retrying. Everything else
// (5xx, network errors with no status) is treated as transient.
func (e *APIError) Retryable() bool {
	if e.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return e.StatusCode < 400 || e.StatusCode >= 500
}

// retryable reports whether an arbitrary error should be retried. A typed
// APIError decides for itself; any other error (e.g. a transport/network
// failure) is considered transient and retried.
func retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Retryable()
	}
	return true
}

// isTemperatureError reports whether a 400 was caused specifically by the
// request's temperature (some models accept only their default). The provider
// uses this to auto-recover by re-sending without the temperature field.
func isTemperatureError(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) || ae.StatusCode != http.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(ae.Body), "temperature")
}
