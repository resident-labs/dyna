package websvc

import (
	"context"
	"errors"
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
