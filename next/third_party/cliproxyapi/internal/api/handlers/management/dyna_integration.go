//go:build dyna_integration

package management

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// DynaIntegrationHarness connects a fake Kimi provider to the real management completion.
type DynaIntegrationHarness struct{ handler *Handler }

func NewDynaIntegrationHarness(store coreauth.Store, notify func(string, string)) *DynaIntegrationHarness {
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	h.tokenStore = store
	h.postAuthHook = func(_ context.Context, a *coreauth.Auth) error { a.Disabled = true; return nil }
	h.postAuthPersistHook = func(ctx context.Context, a *coreauth.Auth) error {
		_, err := manager.Register(coreauth.WithSkipPersist(ctx), a)
		return err
	}
	DynaMemoryOAuthCallbacks = true
	DynaOAuthCompletionHook = notify
	return &DynaIntegrationHarness{handler: h}
}

func (h *DynaIntegrationHarness) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v8/management/oauth/auth-url" {
		RegisterOAuthSession("invented-state", "kimi")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://provider.example/login","state":"invented-state","user_code":"invented-code"}`))
		return
	}
	if r.URL.Path == "/v8/management/oauth/status" {
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		h.handler.GetAuthStatus(c)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (h *DynaIntegrationHarness) Complete() error {
	record := &coreauth.Auth{ID: "invented-id", Provider: "kimi", Storage: &kimi.KimiTokenStorage{AccessToken: "invented-token", RefreshToken: "invented-refresh"}}
	if err := claimOAuthSessionCommit("invented-state", "kimi"); err != nil {
		return err
	}
	if _, err := h.handler.saveTokenRecord(context.Background(), record); err != nil {
		return err
	}
	h.handler.dynaCompleteOAuth("invented-state", record)
	return nil
}

func (h *DynaIntegrationHarness) Close() {
	DynaMemoryOAuthCallbacks = false
	DynaOAuthCompletionHook = nil
	oauthSessions.mu.Lock()
	delete(oauthSessions.sessions, "invented-state")
	oauthSessions.mu.Unlock()
}
