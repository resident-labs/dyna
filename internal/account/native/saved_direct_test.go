package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type directTestClient func(*http.Request) (*http.Response, error)

func (f directTestClient) Do(r *http.Request) (*http.Response, error) { return f(r) }
func directTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func directTestAgy(t *testing.T, expiry time.Time) Captured {
	t.Helper()
	raw := agyTestWrapper(t, "saved")
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	var token map[string]json.RawMessage
	_ = json.Unmarshal(obj["token"], &token)
	token["expiry"], _ = json.Marshal(expiry.Format(time.RFC3339Nano))
	obj["token"], _ = json.Marshal(token)
	raw, _ = json.Marshal(obj)
	c, err := agyAccount(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestSavedClaudeUsesSavedTokenAndCurrentParsing(t *testing.T) {
	m := claudeTestManager(t, nil)
	credential := claudeTestPayload(t, m, claudeTestAccountA, "saved", time.Hour)
	c := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: credential}
	before := append([]byte(nil), credential.Payload...)
	calls := 0
	m.client = directTestClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("User-Agent") != claudeUserAgent || r.Method != "GET" || r.URL.String() != claudeUsageURL || r.Header.Get("Authorization") != "Bearer fixture-access-saved" || r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Error("wrong saved Claude request")
		}
		return directTestResponse(200, `{"five_hour":{"utilization":0.5,"resets_at":"2026-10-17T15:00:00Z"},"seven_day":{"utilization":50},"seven_day_oauth_apps":{"utilization":12},"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"id":"opus"}}}]}`), nil
	})
	o, err := m.ProbeSaved(context.Background(), c)
	if err != nil || calls != 1 || len(o.Windows) != 3 || *o.Windows[0].UsedPercent != 0.5 || *o.Windows[1].UsedPercent != 12 || *o.Windows[2].UsedPercent != 0 || o.Plan == nil || o.Plan.ID != "fixture-tier" || !o.ObservedAt.Equal(m.now()) {
		t.Fatalf("unexpected Claude observation: %+v, %v", o, err)
	}
	if !reflect.DeepEqual(before, []byte(c.Credential.Payload)) {
		t.Fatal("probe mutated saved payload")
	}
	if _, err := os.Stat(filepath.Join(m.host.Home, ".claude")); !os.IsNotExist(err) {
		t.Fatal("probe created native state")
	}
}
func TestSavedAntigravityLoadsPlanAndGroupedQuotaWithoutNativeStore(t *testing.T) {
	c := directTestAgy(t, time.Now().Add(time.Hour))
	store := &agyFakeStore{readErr: errors.New("must not read native store")}
	m := agyTestManager(t, store)
	calls := 0
	m.client = directTestClient(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fake-access-saved" {
			t.Error("wrong Antigravity auth")
		}
		if calls == 1 {
			if r.URL.String() != agyCloudURL+"loadCodeAssist" || !strings.Contains(string(body), `"ideType":"ANTIGRAVITY"`) {
				t.Error("wrong load request")
			}
			return directTestResponse(200, `{"cloudaicompanionProject":"observed-project","currentTier":{"id":"free-tier"},"paidTier":{"id":"g1-pro-tier","name":"Google AI Pro"}}`), nil
		}
		if calls != 2 || r.URL.String() != agyCloudURL+"retrieveUserQuotaSummary" || string(body) != `{"project":"observed-project"}` {
			t.Error("wrong quota request")
		}
		return directTestResponse(200, `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","displayName":"Session","window":"5h","remainingFraction":0.25,"resetTime":"2026-10-17T16:00:00Z"},{"bucketId":"gemini-weekly","remainingFraction":1},{"bucketId":"disabled","remainingFraction":0,"disabled":true},{"bucketId":"unknown"},{"bucketId":"invalid","remainingFraction":2}]}],"buckets":[{"bucketId":"gemini-5h","remainingFraction":0},{"bucketId":"fallback","remainingFraction":0}]}`), nil
	})
	o, err := m.ProbeSaved(context.Background(), c)
	if err != nil || calls != 2 || o.Plan == nil || o.Plan.ID != "g1-pro-tier" || o.Plan.Label != "Google AI Pro" || len(o.Windows) != 3 || *o.Windows[0].UsedPercent != 75 || *o.Windows[1].UsedPercent != 0 || *o.Windows[2].UsedPercent != 100 {
		t.Fatalf("unexpected Antigravity observation: %+v, %v", o, err)
	}
	if store.reads != 0 || store.writes != 0 {
		t.Fatal("probe accessed native credentials")
	}
	if plan := m.CachedPlan(c); plan == nil || plan.ID != "fake-tier" {
		t.Fatal("cached tier unavailable")
	}
}
func TestSavedProbesRejectExpiredAndMismatchedCredentialsWithoutRequests(t *testing.T) {
	now := time.Now().UTC()
	cm := claudeTestManager(t, nil)
	claude := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, cm, claudeTestAccountA, "expired", 30*time.Second)}
	agy := directTestAgy(t, now.Add(-time.Hour))
	am := agyTestManager(t, &agyFakeStore{})
	deny := directTestClient(func(*http.Request) (*http.Response, error) {
		t.Error("probe must not refresh or request quota")
		return nil, errors.New("unexpected request")
	})
	cm.client = deny
	am.client = deny
	for _, tc := range []struct {
		name string
		c    Captured
		m    SavedProber
	}{{"claude", claude, cm}, {"agy", agy, am}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.m.ProbeSaved(context.Background(), tc.c); !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("expired token: %v", err)
			}
			tc.c.Identity = "other"
			if _, err := tc.m.ProbeSaved(context.Background(), tc.c); !errors.Is(err, ErrNoIdentity) {
				t.Fatalf("mismatched identity: %v", err)
			}
		})
	}
}
func TestSavedProbesReturnTypedRetryErrorsAndHideProviderText(t *testing.T) {
	now := time.Now().UTC()
	cm := claudeTestManager(t, nil)
	claude := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, cm, claudeTestAccountA, "saved", time.Hour)}
	am := agyTestManager(t, &agyFakeStore{})
	for _, status := range []int{429, 500, 503} {
		for _, tc := range []struct {
			name string
			c    Captured
			m    SavedProber
			set  func(claudeHTTPClient)
		}{{"claude", claude, cm, func(c claudeHTTPClient) { cm.client = c }}, {"agy", directTestAgy(t, now.Add(time.Hour)), am, func(c claudeHTTPClient) { am.client = c }}} {
			t.Run(tc.name+http.StatusText(status), func(t *testing.T) {
				tc.set(directTestClient(func(*http.Request) (*http.Response, error) {
					r := directTestResponse(status, "private-provider-text")
					r.Header.Set("Retry-After", "30")
					return r, nil
				}))
				_, err := tc.m.ProbeSaved(context.Background(), tc.c)
				var qe *QuotaError
				if !errors.As(err, &qe) || qe.Status != status || qe.RetryAfter != 30*time.Second || strings.Contains(err.Error(), "private-provider-text") {
					t.Fatalf("wrong retry error: %v", err)
				}
			})
		}
	}
}
func TestSavedAntigravityRefreshPreservesRefreshAndUnknownFields(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	c := directTestAgy(t, now.Add(-time.Hour))
	before := append([]byte(nil), c.Credential.Payload...)
	store := &agyFakeStore{}
	m := agyTestManager(t, store)
	m.now = func() time.Time { return now }
	m.clientSecret = func(context.Context) (string, error) { return "fake-client-secret", nil }
	m.client = directTestClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != agyTokenURL || r.Method != "POST" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Error("wrong refresh endpoint")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("client_id") != agyClientID || r.Form.Get("client_secret") != "fake-client-secret" || r.Form.Get("refresh_token") != "fake-refresh-saved" || r.Form.Get("grant_type") != "refresh_token" {
			t.Error("wrong refresh form")
		}
		return directTestResponse(200, `{"access_token":"new-access","expires_in":3599,"token_type":"Bearer"}`), nil
	})
	next, err := m.Refresh(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	wrapper, token, _, err := savedAgy(next)
	if err != nil || jsonString(token["access_token"]) != "new-access" || jsonString(token["refresh_token"]) != "fake-refresh-saved" || wrapper["future_field"] == nil || !next.ExpiresAt.Equal(now.Add(3599*time.Second)) {
		t.Fatal("refresh lost wrapper fields, refresh token, or expiry")
	}
	if !reflect.DeepEqual(before, []byte(c.Credential.Payload)) || store.reads != 0 || store.writes != 0 {
		t.Fatal("refresh changed input or native credentials")
	}
}
func TestSavedAntigravityRefreshErrorsAndFreshGrant(t *testing.T) {
	now := time.Now().UTC()
	c := directTestAgy(t, now.Add(-time.Hour))
	m := agyTestManager(t, &agyFakeStore{})
	m.now = func() time.Time { return now }
	m.clientSecret = func(context.Context) (string, error) { return "fake", nil }
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		invalid bool
	}{{"revoked", 400, `{"error":"invalid_grant","error_description":"private"}`, true}, {"limited", 429, "private", false}, {"server", 503, "private", false}} {
		t.Run(tc.name, func(t *testing.T) {
			m.client = directTestClient(func(*http.Request) (*http.Response, error) {
				r := directTestResponse(tc.status, tc.body)
				r.Header.Set("Retry-After", now.Add(time.Minute).Format(http.TimeFormat))
				return r, nil
			})
			next, err := m.Refresh(context.Background(), c)
			if err == nil || !reflect.DeepEqual(next, c) || strings.Contains(err.Error(), "private") {
				t.Fatal("refresh error exposed text or changed credential")
			}
			if tc.invalid {
				if !errors.Is(err, ErrInvalidGrant) {
					t.Fatal("invalid grant not classified")
				}
			} else {
				var qe *QuotaError
				if !errors.As(err, &qe) || qe.Status != tc.status || qe.RetryAfter < 59*time.Second || qe.RetryAfter > time.Minute {
					t.Fatalf("wrong quota error: %v", err)
				}
			}
		})
	}
	m.clientSecret = func(context.Context) (string, error) {
		t.Error("fresh grant must not extract secret")
		return "", ErrUnsupported
	}
	if _, err := m.Refresh(context.Background(), directTestAgy(t, now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
}
func TestSavedAntigravityRuntimeSecretExtraction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agy")
	a := "GOCSPX-" + strings.Repeat("a", 28)
	b := "GOCSPX-" + strings.Repeat("b", 28)
	c := "GOCSPX-" + strings.Repeat("c", 28)
	// Split a secret across the reader boundary, and repeat another secret.
	contents := strings.Repeat("!", (64<<10)-10) + c + "!" + agyClientID + "!" + b + "!" + a + "!" + c
	if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	m := &antigravityManager{host: Host{Executable: path}}
	secret, err := m.oauthClientSecret(context.Background())
	if err != nil || secret != b {
		t.Fatal("runtime extraction failed")
	}
	if err := os.WriteFile(path, []byte(agyClientID+"!"+a), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.oauthClientSecret(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatal("accepted unknown binary layout")
	}
}
func TestSavedClaudeRefreshReturnsTypedRetryError(t *testing.T) {
	m := claudeTestManager(t, nil)
	c := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, m, claudeTestAccountA, "expired", -time.Hour)}
	m.client = directTestClient(func(*http.Request) (*http.Response, error) {
		r := directTestResponse(503, "private")
		r.Header.Set("Retry-After", "45")
		return r, nil
	})
	_, err := m.Refresh(context.Background(), c)
	var qe *QuotaError
	if !errors.As(err, &qe) || qe.Status != 503 || qe.RetryAfter != 45*time.Second {
		t.Fatalf("wrong Claude refresh retry error: %v", err)
	}
}

func TestSavedProbesKeepMalformedQuotaUnknown(t *testing.T) {
	cm := claudeTestManager(t, nil)
	claude := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, cm, claudeTestAccountA, "saved", time.Hour)}
	am := agyTestManager(t, &agyFakeStore{})
	cases := []struct {
		name     string
		captured Captured
		probe    SavedProber
		set      func(claudeHTTPClient)
	}{
		{"claude", claude, cm, func(c claudeHTTPClient) { cm.client = c }},
		{"agy", directTestAgy(t, time.Now().Add(time.Hour)), am, func(c claudeHTTPClient) { am.client = c }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, body := range []string{`{}`, `null`, `{"planUsage":{"totalPercentUsed":null}}`} {
				tc.set(directTestClient(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, ":loadCodeAssist") {
						return directTestResponse(200, `{"cloudaicompanionProject":"p"}`), nil
					}
					return directTestResponse(200, body), nil
				}))
				o, err := tc.probe.ProbeSaved(context.Background(), tc.captured)
				if err == nil || len(o.Windows) != 0 {
					t.Fatal("malformed response became known usage")
				}
			}
			tc.set(directTestClient(func(*http.Request) (*http.Response, error) {
				t.Error("cancelled probe sent request")
				return nil, context.Canceled
			}))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := tc.probe.ProbeSaved(ctx, tc.captured); !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled probe lost context error")
			}
		})
	}
}

func TestSavedAntigravityRefreshAcceptsRotationAndRejectsChangedIdentity(t *testing.T) {
	now := time.Now().UTC()
	c := directTestAgy(t, now.Add(-time.Hour))
	m := agyTestManager(t, &agyFakeStore{})
	m.now = func() time.Time { return now }
	m.clientSecret = func(context.Context) (string, error) { return "fake", nil }
	for _, sub := range []string{"saved", "other"} {
		t.Run(sub, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"access_token": "rotated-access", "refresh_token": "rotated-refresh", "expires_in": 3600, "id_token": testJWT(sub, 4102444800)})
			m.client = directTestClient(func(*http.Request) (*http.Response, error) { return directTestResponse(200, string(raw)), nil })
			next, err := m.Refresh(context.Background(), c)
			if sub == "other" {
				if !errors.Is(err, ErrNoIdentity) || !reflect.DeepEqual(c, next) {
					t.Fatal("accepted refreshed identity change")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, token, _, err := savedAgy(next)
			if err != nil || jsonString(token["refresh_token"]) != "rotated-refresh" {
				t.Fatal("lost rotated refresh token")
			}
		})
	}
}

func TestActiveClaudeUsageReturnsTypedRetryErrors(t *testing.T) {
	m := claudeTestManager(t, nil)
	claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "active", time.Hour))
	for _, status := range []int{429, 503} {
		client := &http.Client{Transport: directTestTransport(func(*http.Request) (*http.Response, error) {
			r := directTestResponse(status, "private-provider-text")
			r.Header.Set("Retry-After", "30")
			return r, nil
		})}
		_, err := FetchClaudeUsage(context.Background(), m.host, ClaudeUsageOptions{Client: client, Now: m.now})
		var qe *QuotaError
		if !errors.As(err, &qe) || qe.Status != status || qe.RetryAfter != 30*time.Second {
			t.Fatalf("wrong active usage error: %v", err)
		}
	}
}

type directTestTransport func(*http.Request) (*http.Response, error)

func (f directTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
