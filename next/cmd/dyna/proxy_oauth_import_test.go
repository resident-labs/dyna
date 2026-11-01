package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"golang.org/x/crypto/bcrypt"
)

type importTestStore struct{}

func (importTestStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (importTestStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	return auth.ID, nil
}
func (importTestStore) Delete(context.Context, string) error { return nil }

type importTestTransport struct{ host *string }

func (t importTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*t.host = r.URL.Host
	return nil, errors.New("fake upstream stopped")
}

type importTestTransportProvider struct{ host *string }

func (p importTestTransportProvider) RoundTripperFor(*coreauth.Auth) http.RoundTripper {
	return importTestTransport{p.host}
}

func TestImportedXAIRoutesToGrokBeforeRefresh(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(importTestStore{}, nil, nil)
	service, err := cliproxy.NewBuilder().WithCoreAuthManager(manager).WithConfig(&config.Config{AuthDir: t.TempDir()}).WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).Build()
	if err != nil {
		t.Fatal(err)
	}
	var host string
	manager.SetRoundTripperProvider(importTestTransportProvider{&host})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	hash, err := bcrypt.GenerateFromPassword([]byte("invented"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	proxyKeyRoutes(router, &service, nil, string(hash))
	id := "dyna-oauth-0123456789abcdef0123456789abcdef"
	body := `{"id":"` + id + `","provider":"xai","expiresAt":"2099-01-01T00:00:00Z","credential":"{\"access_token\":\"invented-access\",\"refresh_token\":\"invented-refresh\"}"}`
	req := httptest.NewRequest(http.MethodPost, "/dyna/oauth/import", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer invented")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatal("staging failed")
	}
	if err := service.ActivateDynaCredential(ctx, id, ""); err != nil {
		t.Fatal("activation failed")
	}
	defer cliproxy.GlobalModelRegistry().UnregisterClient(id)
	_, _ = manager.Execute(ctx, []string{"xai"}, executor.Request{Model: "grok-4.3", Payload: []byte(`{"model":"grok-4.3","input":"hello"}`)}, executor.Options{SourceFormat: translator.FormatOpenAIResponse})
	if host != "cli-chat-proxy.grok.com" {
		t.Fatalf("request used host %q, want Grok CLI proxy", host)
	}
}
