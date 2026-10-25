package native

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

func piCaptured(t *testing.T, entryID string, entry piAuthEntry) Captured {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"auth.json": map[string]piAuthEntry{entryID: entry}})
	if err != nil {
		t.Fatal(err)
	}
	return Captured{Identity: "pi:" + entryID, Credential: store.Credential{Schema: piNativeFilesSchema, Payload: payload}}
}

func piTestJWT(claims map[string]any) string {
	raw, _ := json.Marshal(claims)
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
}

func TestPiChatGPTUsageReadsTheCodexPlan(t *testing.T) {
	c := piCaptured(t, "openai-codex", piAuthEntry{
		Type: "oauth", Access: "fixture-access", AccountID: "fixture-account", Expires: 1 << 62,
	})
	before := append([]byte(nil), c.Credential.Payload...)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	calls := 0
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || r.Header.Get("Authorization") != "Bearer fixture-access" || r.Header.Get("ChatGPT-Account-Id") != "fixture-account" {
			t.Fatal("wrong ChatGPT usage request")
		}
		return directTestResponse(200, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_at":1900000000},"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_at":1900000000}}}`), nil
	})
	o, err := probePiSaved(context.Background(), c, client, now)
	if err != nil || calls != 1 || len(o.Windows) != 2 || *o.Windows[0].UsedPercent != 12 || o.Plan == nil || o.Plan.ID != "pro" {
		t.Fatalf("unexpected ChatGPT observation: %+v, %v", o, err)
	}
	if !reflect.DeepEqual(before, []byte(c.Credential.Payload)) {
		t.Fatal("probe mutated the saved credential")
	}
}

func TestPiKimiCodeUsageReadsTheCodingPlan(t *testing.T) {
	c := piCaptured(t, "kimi-coding", piAuthEntry{Type: "oauth", Access: "fixture-access"})
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.kimi.com/coding/v1/usages" || r.Header.Get("Authorization") != "Bearer fixture-access" {
			t.Fatal("wrong Kimi usage request")
		}
		return directTestResponse(200, `{"usages":{"limit_5h":{"used_ratio":0.2,"reset_time":"2026-10-25T15:00:00Z"},"limit_7d":{"used_ratio":0.3,"reset_time":"2026-10-30T00:00:00Z"}}}`), nil
	})
	o, err := probePiSaved(context.Background(), c, client, now)
	if err != nil || len(o.Windows) != 2 || *o.Windows[0].UsedPercent != 20 || *o.Windows[1].UsedPercent != 30 {
		t.Fatalf("unexpected Kimi observation: %+v, %v", o, err)
	}
}

func TestPiXAIUsageReadsTheGrokCredits(t *testing.T) {
	token := piTestJWT(map[string]any{"sub": "fixture-user", "exp": int64(1 << 62)})
	c := piCaptured(t, "xai", piAuthEntry{Type: "oauth", Access: token})
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://cli-chat-proxy.grok.com/v1/billing?format=credits" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("x-userid") != "fixture-user" {
			t.Fatal("wrong xAI usage request")
		}
		return directTestResponse(200, grokWeeklyFixture), nil
	})
	o, err := probePiSaved(context.Background(), c, client, now)
	if err != nil || len(o.Windows) != 1 || o.Windows[0].ID != "grok-weekly" || o.Windows[0].Label != "Weekly" || o.Windows[0].UsedPercent == nil || *o.Windows[0].UsedPercent != 0 || o.Plan == nil || o.Plan.ID != "x-subscription" {
		t.Fatalf("unexpected xAI observation: %+v, %v", o, err)
	}
}

func TestPiOpenCodeGoUsageReadsTheGoPlan(t *testing.T) {
	c := piCaptured(t, "opencode-go", piAuthEntry{Type: "api_key", Key: "fixture-key"})
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://opencode.ai/inference/go/v1/usage" || r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("User-Agent") != "opencode/latest/2.0.32/cli" {
			t.Fatal("wrong OpenCode Go usage request")
		}
		return directTestResponse(200, `{"usage":{"rolling":{"percent":0},"weekly":{"percent":100,"resetsAt":"2026-10-26T00:00:00Z"},"monthly":{"percent":92}}}`), nil
	})
	o, err := probePiSaved(context.Background(), c, client, now)
	if err != nil || len(o.Windows) != 3 || *o.Windows[0].UsedPercent != 0 || *o.Windows[1].UsedPercent != 100 || *o.Windows[2].UsedPercent != 92 {
		t.Fatalf("unexpected OpenCode Go observation: %+v, %v", o, err)
	}
}

func TestPiDeepSeekUsageReadsTheBalance(t *testing.T) {
	c := piCaptured(t, "deepseek", piAuthEntry{Type: "api_key", Key: "fixture-key"})
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.deepseek.com/user/balance" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Fatal("wrong DeepSeek balance request")
		}
		return directTestResponse(200, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"53.78","granted_balance":"0.00","topped_up_balance":"53.78"}]}`), nil
	})
	o, err := probePiSaved(context.Background(), c, client, now)
	if err != nil || len(o.Windows) != 1 || o.Windows[0].Balance == nil || *o.Windows[0].Balance != 53.78 || o.Windows[0].Unit != "USD" {
		t.Fatalf("unexpected DeepSeek observation: %+v, %v", o, err)
	}
}

func TestPiZAIUsageReadsTheCodingPlanLimits(t *testing.T) {
	c := piCaptured(t, "zai", piAuthEntry{Type: "api_key", Key: "fixture-key"})
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.z.ai/api/monitor/usage/quota/limit" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Fatal("wrong z.ai usage request")
		}
		return directTestResponse(200, `{"code":200,"success":true,"msg":"Operation successful","data":{"limits":[{"type":"TIME_LIMIT","unit":5,"number":1,"usage":4000,"currentValue":0,"remaining":4000,"percentage":0,"nextResetTime":1795083286997},{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":1,"nextResetTime":1792961857271}],"level":"max"}}`), nil
	})
	o, err := probePiSaved(context.Background(), c, client, now)
	if err != nil || o.Plan == nil || o.Plan.ID != "glm-coding-max" || len(o.Windows) != 2 {
		t.Fatalf("unexpected z.ai observation: %+v, %v", o, err)
	}
	if o.Windows[0].ID != "monthly_tools" || o.Windows[0].Used == nil || *o.Windows[0].Used != 0 || o.Windows[0].Limit == nil || *o.Windows[0].Limit != 4000 {
		t.Fatalf("wrong z.ai tool window: %+v", o.Windows[0])
	}
	if o.Windows[1].ID != "five_hour" || o.Windows[1].UsedPercent == nil || *o.Windows[1].UsedPercent != 1 {
		t.Fatalf("wrong z.ai token window: %+v", o.Windows[1])
	}
}

func TestPiMetaOAuthUsageUsesTheIdentityToken(t *testing.T) {
	c := piCaptured(t, "meta", piAuthEntry{
		Type: "oauth", Access: "fixture-minted-key", Refresh: "fixture-identity",
	})
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.String() != "https://api.meta.ai/muse-code/key" || r.Header.Get("Authorization") != "Bearer fixture-identity" || string(body) != `{"onboard":false}` {
			t.Fatal("wrong Muse OAuth usage request")
		}
		return directTestResponse(200, `{"user_email":"fixture@example.test","subs_tier_id":"max","subs_tier_name":"Muse Code Max Usage","is_subs_active":true,"subs_usage":{"window":{"used_percent":12.5,"window_duration_mins":300,"resets_at":2000000000},"weekly":{"used_percent":45,"resets_at":2000600000}}}`), nil
	})
	o, err := probePiSaved(context.Background(), c, client, now)
	if err != nil || len(o.Windows) != 2 || *o.Windows[0].UsedPercent != 12.5 || *o.Windows[1].UsedPercent != 45 || o.Plan == nil || o.Plan.Label != "Muse Code Max Usage" {
		t.Fatalf("unexpected Muse OAuth observation: %+v, %v", o, err)
	}
}

func TestPiMetaOAuthIdlePlanIsKept(t *testing.T) {
	c := piCaptured(t, "meta", piAuthEntry{
		Type: "oauth", Access: "fixture-minted-key", Refresh: "fixture-identity",
	})
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer fixture-identity" {
			t.Fatal("wrong Muse OAuth usage request")
		}
		return directTestResponse(200, `{"user_email":"fixture@example.test","subs_tier_id":"max","subs_tier_name":"Muse Code Max Usage","is_subs_active":true}`), nil
	})
	o, err := probePiSaved(context.Background(), c, client, now)
	if !errors.Is(err, ErrUsageIdle) || o.Plan == nil || o.Plan.Label != "Muse Code Max Usage" || !o.ObservedAt.IsZero() || len(o.Windows) != 0 {
		t.Fatalf("unexpected Muse idle observation: %+v, %v", o, err)
	}
}

func TestPiMetaKeyIsTypedNotReadableWithoutARequest(t *testing.T) {
	c := piCaptured(t, "meta", piAuthEntry{Type: "api_key", Key: "fixture-key"})
	deny := directTestClient(func(*http.Request) (*http.Response, error) {
		t.Error("a Meta API key must not request usage")
		return nil, errors.New("unexpected request")
	})
	_, err := probePiSaved(context.Background(), c, deny, time.Now().UTC())
	var unavailable *UsageUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != store.UsageReasonNotReadable {
		t.Fatalf("Meta key result = %v", err)
	}
}

func TestPiUsageRejectsAnotherAccountsCredential(t *testing.T) {
	c := piCaptured(t, "deepseek", piAuthEntry{Type: "api_key", Key: "fixture-key"})
	c.Identity = "pi:zai"
	deny := directTestClient(func(*http.Request) (*http.Response, error) {
		t.Error("a mismatched credential must not request usage")
		return nil, errors.New("unexpected request")
	})
	if _, err := probePiSaved(context.Background(), c, deny, time.Now().UTC()); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("mismatched identity = %v", err)
	}
}

func TestPiUsageNamesAnExpiredSignInWithoutSigningOut(t *testing.T) {
	c := piCaptured(t, "xai", piAuthEntry{Type: "oauth", Access: piTestJWT(map[string]any{"sub": "fixture-user"})})
	client := directTestClient(func(*http.Request) (*http.Response, error) {
		return directTestResponse(401, "private-provider-error"), nil
	})
	_, err := probePiSaved(context.Background(), c, client, time.Now().UTC())
	var unavailable *UsageUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != store.UsageReasonExpired || strings.Contains(err.Error(), "private-provider-error") {
		t.Fatalf("expired sign-in result = %v", err)
	}
}
