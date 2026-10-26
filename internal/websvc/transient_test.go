package websvc

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"dyna-agent/internal/callspec"
)

// A retryable write condition answers 503, so the caller keeps the request.
func TestRetryableWritesAreTransient(t *testing.T) {
	if !Transient(callspec.Retryable(errors.New("room log unavailable"))) {
		t.Fatal("a retryable write condition is not transient")
	}
	if Transient(errors.New("a refusal")) {
		t.Fatal("a plain refusal is transient")
	}
	if !Transient(context.DeadlineExceeded) {
		t.Fatal("a deadline is not transient")
	}
}

// A call whose request context ended gets a retryable 503, even when its error
// lost the cause and only carries the text (D-269).
func TestCallCanceledByEngineStopAnswers503WithCode(t *testing.T) {
	status, code := callFailure(context.Canceled, errors.New("context canceled"))
	if status != http.StatusServiceUnavailable || code != callspec.CodeEngineStopping {
		t.Fatalf("canceled call: %d %q", status, code)
	}
	if status, code := callFailure(nil, errors.New("a refusal")); status != http.StatusUnprocessableEntity || code != "" {
		t.Fatalf("refusal: %d %q", status, code)
	}
}
