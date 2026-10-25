package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

// piLoginOf builds the credential of a Pi account that signed in to one
// provider, exactly as the Pi capture writes it.
func piLoginOf(t *testing.T, id string, entry map[string]any) Captured {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"auth.json": map[string]any{id: entry}})
	if err != nil {
		t.Fatal(err)
	}
	return Captured{Identity: "pi:" + id, Credential: store.Credential{Schema: piFilesSchema, Payload: payload}}
}

func piAccessToken(claims map[string]any) map[string]any {
	return map[string]any{"type": "oauth", "access": testJWT(claims), "refresh": "refresh-token", "expires": time.Now().Add(time.Hour).UnixMilli()}
}

func TestPiKimiCodeUsage(t *testing.T) {
	const usage = `{"usages":{"limit_5h":{"used_ratio":0.25,"reset_time":"2026-11-01T13:30:39Z"},"limit_7d":{"used_ratio":0.5,"reset_time":"2026-11-08T13:30:39Z"}}}`
	calls := 0
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = chatGPTUsageTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.String() != kimiCodeUsageURL || req.Header.Get("Authorization") != "Bearer "+testJWT(map[string]any{"sub": "kimi-user"}) {
			t.Fatal("wrong Kimi usage request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(usage))}, nil
	})
	login := piLoginOf(t, "kimi-coding", piAccessToken(map[string]any{"sub": "kimi-user"}))
	observation, err := ProbePiSaved(context.Background(), login)
	if err != nil || calls != 1 || len(observation.Windows) != 2 || observation.Windows[0].ID != "five_hour" || *observation.Windows[1].UsedPercent != 50 {
		t.Fatalf("calls=%d observation=%+v err=%v", calls, observation, err)
	}
}

func TestPiKimiCodeUsageRefusesExpiredGrant(t *testing.T) {
	entry := map[string]any{"type": "oauth", "access": testJWT(map[string]any{"sub": "kimi-user"}), "refresh": "refresh-token", "expires": time.Now().Add(-time.Hour).UnixMilli()}
	calls := 0
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = chatGPTUsageTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not call the provider")
	})
	_, err := ProbePiSaved(context.Background(), piLoginOf(t, "kimi-coding", entry))
	var httpErr *UsageHTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestPiGrokUsage(t *testing.T) {
	const billing = `{"config":{"isUnifiedBillingUser":true,"creditUsagePercent":12.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-20T00:00:00Z","end":"2026-10-27T00:00:00Z"}}}`
	calls := 0
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = chatGPTUsageTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "cli-chat-proxy.grok.com" || req.Header.Get("x-userid") != "grok-user" || req.Header.Get("x-xai-token-auth") != "xai-grok-cli" {
			t.Fatal("wrong Grok billing request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(billing))}, nil
	})
	entry := piAccessToken(map[string]any{"principal_id": "grok-user", "sub": "someone-else"})
	observation, err := ProbePiSaved(context.Background(), piLoginOf(t, "xai", entry))
	if err != nil || calls != 1 || len(observation.Windows) == 0 || *observation.Windows[0].UsedPercent != 12.5 {
		t.Fatalf("calls=%d observation=%+v err=%v", calls, observation, err)
	}
}

func TestPiGrokUsageNeedsAUserID(t *testing.T) {
	_, err := ProbePiSaved(context.Background(), piLoginOf(t, "xai", piAccessToken(map[string]any{"sub": ""})))
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("want ErrNoIdentity, got %v", err)
	}
}

func TestPiAPIKeyAccountReportsNoUsage(t *testing.T) {
	for _, id := range []string{"deepseek", "zai", "meta", "opencode-go"} {
		t.Run(id, func(t *testing.T) {
			login := piLoginOf(t, id, map[string]any{"type": "api_key", "key": "sk-private"})
			if _, err := ProbePiSaved(context.Background(), login); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("want ErrUnsupported, got %v", err)
			}
			if CredentialUsesAPIKey(login) != true {
				t.Fatal("a Pi key entry is an API-key credential")
			}
		})
	}
}
