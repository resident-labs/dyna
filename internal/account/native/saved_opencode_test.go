package native

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type openCodeUsageTransport func(*http.Request) (*http.Response, error)

func (f openCodeUsageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenCodeSavedUsageUsesExactPrivateReference(t *testing.T) {
	m := openCodeTestManager(t)
	raw, err := os.ReadFile(m.host.Executable)
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1 $2" = "auth export" ]; then
 printf '%s\n' '[{"id":"cred_old","integrationID":"opencode-go","value":{"type":"key","key":"fixture-old"}},{"id":"cred_new","integrationID":"opencode-go","value":{"type":"key","key":"fixture-new"}}]'
 exit 0
fi
` + strings.TrimPrefix(string(raw), "#!/bin/sh\n")
	if err = os.WriteFile(m.host.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	captured, err := m.ListNative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	calls := 0
	http.DefaultTransport = openCodeUsageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://opencode.ai/inference/go/v1/usage" || r.Header.Get("Authorization") != "Bearer fixture-new" || r.Header.Get("User-Agent") != "opencode/latest/2.0.32/cli" {
			t.Fatal("quota used wrong credential or endpoint")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"usage":{"rolling":{"percent":29},"weekly":{"percent":0}}}`))}, nil
	})
	prober, ok := any(m).(SavedProber)
	if !ok {
		t.Fatal("OpenCode manager does not implement SavedProber")
	}
	observation, err := prober.ProbeSaved(context.Background(), captured[1])
	if err != nil || calls != 1 || len(observation.Windows) != 2 || *observation.Windows[0].UsedPercent != 29 || *observation.Windows[1].UsedPercent != 0 {
		t.Fatalf("quota=%+v calls=%d err=%v", observation, calls, err)
	}
	var payload map[string]any
	_ = json.Unmarshal(captured[1].Credential.Payload, &payload)
	payload["ref"].(map[string]any)["scope"].(map[string]any)["home"] = t.TempDir()
	captured[1].Credential.Payload, _ = json.Marshal(payload)
	if _, err = prober.ProbeSaved(context.Background(), captured[1]); err == nil || calls != 1 {
		t.Fatal("accepted another private account scope")
	}
}
