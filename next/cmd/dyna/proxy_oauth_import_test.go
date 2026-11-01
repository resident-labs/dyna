package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"golang.org/x/crypto/bcrypt"
)

const importTestID = "dyna-oauth-0123456789abcdef0123456789abcdef"

type importTestStore struct{}

func (importTestStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (importTestStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	return auth.ID, nil
}
func (importTestStore) Delete(context.Context, string) error { return nil }

type importTestTransport struct{ seen **http.Request }

func (t importTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*t.seen = r
	return nil, errors.New("fake upstream stopped")
}

type importTestTransportProvider struct{ seen **http.Request }

func (p importTestTransportProvider) RoundTripperFor(*coreauth.Auth) http.RoundTripper {
	return importTestTransport{p.seen}
}

func importTestRoutes(t *testing.T) (*coreauth.Manager, *cliproxy.Service, *gin.Engine) {
	t.Helper()
	manager := coreauth.NewManager(importTestStore{}, nil, nil)
	service, err := cliproxy.NewBuilder().WithCoreAuthManager(manager).WithConfig(&config.Config{AuthDir: t.TempDir()}).WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).Build()
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	hash, err := bcrypt.GenerateFromPassword([]byte("invented"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	proxyKeyRoutes(router, &service, nil, string(hash))
	return manager, service, router
}

func importTestPost(t *testing.T, router *gin.Engine, provider, credential string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"id": importTestID, "provider": provider, "expiresAt": "2099-01-01T00:00:00Z", "credential": credential})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dyna/oauth/import", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer invented")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestImportedOAuthRoutesBeforeRefresh(t *testing.T) {
	for _, test := range []struct {
		provider   string
		proxy      string
		model      string
		credential string
		host       string
		headers    map[string]string
	}{
		{
			provider:   "anthropic",
			proxy:      "claude",
			model:      "claude-sonnet-5",
			credential: `{"access_token":"sk-ant-oat01-invented","refresh_token":"invented-refresh","email":"owner@example.com"}`,
			host:       "api.anthropic.com",
			headers:    map[string]string{"Authorization": "Bearer sk-ant-oat01-invented", "X-Api-Key": ""},
		},
		{
			provider:   "openai",
			proxy:      "codex",
			model:      "gpt-5.6-luna",
			credential: `{"access_token":"invented-access","refresh_token":"invented-refresh","account_id":"invented-account"}`,
			host:       "chatgpt.com",
			headers:    map[string]string{"Authorization": "Bearer invented-access", "Chatgpt-Account-Id": "invented-account"},
		},
		{
			provider:   "kimi",
			proxy:      "kimi",
			model:      "kimi-k2.5",
			credential: `{"access_token":"invented-access","refresh_token":"invented-refresh"}`,
			host:       "api.kimi.com",
			headers:    map[string]string{"Authorization": "Bearer invented-access"},
		},
		{
			provider:   "xai",
			proxy:      "xai",
			model:      "grok-4.3",
			credential: `{"access_token":"invented-access","refresh_token":"invented-refresh"}`,
			host:       "cli-chat-proxy.grok.com",
			headers:    map[string]string{"Authorization": "Bearer invented-access"},
		},
	} {
		t.Run(test.provider, func(t *testing.T) {
			ctx := context.Background()
			manager, service, router := importTestRoutes(t)
			var seen *http.Request
			manager.SetRoundTripperProvider(importTestTransportProvider{&seen})
			if response := importTestPost(t, router, test.provider, test.credential); response.Code != http.StatusOK {
				t.Fatal("staging failed")
			}
			if err := service.ActivateDynaCredential(ctx, importTestID, ""); err != nil {
				t.Fatal("activation failed")
			}
			defer cliproxy.GlobalModelRegistry().UnregisterClient(importTestID)
			auth, ok := manager.GetByID(importTestID)
			if !ok || auth.Provider != test.proxy || auth.Metadata["type"] != test.proxy || auth.AuthKind() != coreauth.AuthKindOAuth || auth.Metadata["refresh_token"] != "invented-refresh" {
				t.Fatal("credential does not match a native record")
			}
			if expiry, ok := auth.ExpirationTime(); !ok || !expiry.Equal(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)) || coreauth.ProviderRefreshLead(test.proxy, nil) == nil {
				t.Fatal("credential is not scheduled for refresh")
			}
			if test.proxy == "claude" {
				// The first real request reads this from the OAuth profile, which skips the fake transport.
				if _, _, err := manager.Modify(ctx, importTestID, 0, func(auth *coreauth.Auth) error {
					auth.Metadata["account_uuid"] = "invented-uuid"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, _ = manager.Execute(ctx, []string{test.proxy}, executor.Request{Model: test.model, Payload: []byte(`{"model":"` + test.model + `","input":"hello"}`)}, executor.Options{SourceFormat: translator.FormatOpenAIResponse})
			if seen == nil {
				t.Fatal("request did not reach the fake upstream")
			}
			if seen.URL.Host != test.host {
				t.Fatalf("request used host %q, want %q", seen.URL.Host, test.host)
			}
			for name, want := range test.headers {
				if got := seen.Header.Get(name); got != want {
					t.Fatalf("request header %s has the wrong shape", name)
				}
			}
		})
	}
}

func TestImportRejectsCredentialForWrongProvider(t *testing.T) {
	for _, test := range []struct {
		name       string
		provider   string
		credential string
	}{
		{"key provider", "zai", `{"access_token":"invented","refresh_token":"invented"}`},
		{"anthropic api key", "anthropic", `{"access_token":"sk-ant-api03-invented","refresh_token":"invented"}`},
		{"anthropic account id", "anthropic", `{"access_token":"sk-ant-oat01-invented","refresh_token":"invented","account_id":"invented"}`},
		{"anthropic bad email", "anthropic", `{"access_token":"sk-ant-oat01-invented","refresh_token":"invented","email":"invented"}`},
		{"openai without account id", "openai", `{"access_token":"invented","refresh_token":"invented"}`},
		{"openai bad account id", "openai", `{"access_token":"invented","refresh_token":"invented","account_id":"invented\r\nx: y"}`},
		{"kimi email", "kimi", `{"access_token":"invented","refresh_token":"invented","email":"owner@example.com"}`},
		{"xai account id", "xai", `{"access_token":"invented","refresh_token":"invented","account_id":"invented"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, router := importTestRoutes(t)
			response := importTestPost(t, router, test.provider, test.credential)
			if response.Code != http.StatusBadRequest || response.Body.Len() != 0 {
				t.Fatalf("got status %d with %d body bytes", response.Code, response.Body.Len())
			}
		})
	}
}
