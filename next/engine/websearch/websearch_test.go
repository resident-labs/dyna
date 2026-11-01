package websearch

import (
	"fmt"
	"strings"
	"testing"
)

// assertRefusal checks that a provider's refusal names its status and the reason
// the provider gave, and that it never carries the secret the request sent. A
// provider's own reason is what makes a refusal fixable.
func assertRefusal(t *testing.T, provider string, status int, secret string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s refusal = nil, want HTTP %d and a reason", provider, status)
	}
	prefix := fmt.Sprintf("%s answered HTTP %d: ", provider, status)
	if !strings.HasPrefix(err.Error(), prefix) || len(err.Error()) == len(prefix) {
		t.Fatalf("err = %v, want the status and the reason", err)
	}
	if secret != "" && strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v, want the secret redacted", err)
	}
}

// A secret that crosses the cut is cleared before the reason is shortened, so no
// fragment of it survives: neither the full replacement nor a pattern would
// match a piece of an opaque key.
func TestStatusErrorRedactsTheWholeSecretBeforeShortening(t *testing.T) {
	const secret = "opaque-plan-key-9f8e7d6c5b4a"
	leader := strings.Repeat("a", maxSnippet-20)
	err := statusError("zai", 503, strings.NewReader(leader+secret), secret)
	if err == nil {
		t.Fatal("no error, want the refusal")
	}
	if strings.Contains(err.Error(), "opaque-plan-key") {
		t.Fatalf("err = %q, want the whole secret redacted", err)
	}
	if !strings.Contains(err.Error(), redacted) {
		t.Fatalf("err = %q, want the redaction marker", err)
	}
	if len([]rune(err.Error())) > maxSnippet+len("zai answered HTTP 503: ")+len(redacted) {
		t.Fatalf("err = %q, want a reason no longer than %d characters", err, maxSnippet)
	}
}

// A refusal keeps the reason the body names, from any of the shapes provider
// APIs use.
func TestStatusErrorReadsTheReasonFromEachBodyShape(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "openai", body: `{"error":{"message":"model is not supported"}}`, want: "model is not supported"},
		{name: "detail", body: `{"detail":"model is not supported"}`, want: "model is not supported"},
		{name: "message", body: `{"message":"model is not supported"}`, want: "model is not supported"},
		{name: "plain", body: "model is not supported", want: "model is not supported"},
		{name: "empty", body: "", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := statusError("chatgpt", 400, strings.NewReader(test.body))
			if test.want == "" {
				if err == nil || err.Error() != "chatgpt answered HTTP 400" {
					t.Fatalf("err = %v, want the status only", err)
				}
				return
			}
			if err == nil || err.Error() != "chatgpt answered HTTP 400: "+test.want {
				t.Fatalf("err = %v, want the body's reason", err)
			}
		})
	}
}

// A credential shape is masked even when the request did not name it.
func TestStatusErrorMasksCredentialShapes(t *testing.T) {
	body := `{"detail":"bad bearer AbCdEfGhIjKlMnOp for sk-live-0123456789abcdef"}`
	err := statusError("brave", 401, strings.NewReader(body))
	if err == nil {
		t.Fatal("no error, want the refusal")
	}
	for _, shape := range []string{"AbCdEfGhIjKlMnOp", "sk-live-0123456789abcdef"} {
		if strings.Contains(err.Error(), shape) {
			t.Fatalf("err = %v, want %q masked", err, shape)
		}
	}
}
