package native

import (
	"context"
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
func TestSavedProbesRejectExpiredAndMismatchedCredentialsWithoutRequests(t *testing.T) {
	cm := claudeTestManager(t, nil)
	claude := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, cm, claudeTestAccountA, "expired", 30*time.Second)}
	deny := directTestClient(func(*http.Request) (*http.Response, error) {
		t.Error("probe must not refresh or request quota")
		return nil, errors.New("unexpected request")
	})
	cm.client = deny
	for _, tc := range []struct {
		name string
		c    Captured
		m    SavedProber
	}{{"claude", claude, cm}} {
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
	cm := claudeTestManager(t, nil)
	claude := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, cm, claudeTestAccountA, "saved", time.Hour)}
	for _, status := range []int{429, 500, 503} {
		for _, tc := range []struct {
			name string
			c    Captured
			m    SavedProber
			set  func(claudeHTTPClient)
		}{{"claude", claude, cm, func(c claudeHTTPClient) { cm.client = c }}} {
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
	cases := []struct {
		name     string
		captured Captured
		probe    SavedProber
		set      func(claudeHTTPClient)
	}{
		{"claude", claude, cm, func(c claudeHTTPClient) { cm.client = c }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, body := range []string{`{}`, `null`, `{"planUsage":{"totalPercentUsed":null}}`} {
				tc.set(directTestClient(func(r *http.Request) (*http.Response, error) {
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
