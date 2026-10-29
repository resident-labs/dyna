package websvc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

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

// A write that runs past its deadline answers with the outcome-unknown code and
// message, and stays 422 so the caller does not send it again. A late read keeps
// the plain line and no code.
func TestLateWriteReplyCarriesOutcomeUnknownCode(t *testing.T) {
	old := deadline
	// A zero deadline is already past, so the call ends late with no real wait.
	deadline = func(string) time.Duration { return 0 }
	t.Cleanup(func() { deadline = old })
	stall := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	calls, err := Bind([]Binding{
		{Name: "RemoveRule", Access: Write, Scope: ScopeRead, Method: stall},
		{Name: "Slow", Access: Read, Scope: ScopeRead, Method: stall},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.srv.o.Calls = calls
	e.srv.o.CallScopes = map[string]Scope{"RemoveRule": ScopeRead, "Slow": ScopeRead}
	var write callErrorResponse
	response := e.do(asCaller(e.srv.UnixHandler(), owner), req{url: "http://engine/api/RemoveRule", body: "[]"})
	if err := json.Unmarshal(response.Body.Bytes(), &write); err != nil || response.Code != http.StatusUnprocessableEntity || write.Code != callspec.CodeOutcomeUnknown || !strings.HasPrefix(write.Error, "outcome unknown: engine did not answer RemoveRule in 0 s") {
		t.Fatalf("late write = %d %+v, %v", response.Code, write, err)
	}
	var read callErrorResponse
	response = e.do(asCaller(e.srv.UnixHandler(), owner), req{url: "http://engine/api/Slow", body: "[]"})
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil || response.Code != http.StatusUnprocessableEntity || read.Code != "" || read.Error != "engine did not answer Slow in 0 s" {
		t.Fatalf("late read = %d %+v, %v", response.Code, read, err)
	}
}
