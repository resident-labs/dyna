package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type dynaTestAntigravityOAuth struct{ *antigravity.AntigravityAuth }

func (dynaTestAntigravityOAuth) ExchangeCodeForTokens(context.Context, string, string) (*antigravity.TokenResponse, error) {
	return &antigravity.TokenResponse{AccessToken: "invented-access", RefreshToken: "invented-refresh", ExpiresIn: 3600}, nil
}
func (dynaTestAntigravityOAuth) FetchUserInfo(context.Context, string) (string, error) {
	return "owner@example.test", nil
}
func (dynaTestAntigravityOAuth) FetchProjectID(context.Context, string) (string, error) {
	return "invented-project", nil
}

func TestDynaAntigravityOAuthUsesMemoryAndNotifiesAfterAdoption(t *testing.T) {
	oldEnabled, oldHook, oldFactory := DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook, newAntigravityOAuthService
	defer func() {
		DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook, newAntigravityOAuthService = oldEnabled, oldHook, oldFactory
	}()
	DynaMemoryOAuthCallbacks = true
	newAntigravityOAuthService = func(cfg *config.Config) antigravityOAuthService {
		return dynaTestAntigravityOAuth{antigravity.NewAntigravityAuth(cfg, nil)}
	}
	completed := make(chan oauthSession, 1)
	DynaOAuthCompletionHook = func(state, _ string) {
		session, _ := oauthSessions.Get(state)
		completed <- session
	}

	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = dynaFakeProviderStore{}
	h.postAuthHook = func(_ context.Context, a *coreauth.Auth) error { a.Disabled = true; return nil }
	h.postAuthPersistHook = func(ctx context.Context, a *coreauth.Auth) error {
		_, err := manager.Register(coreauth.WithSkipPersist(ctx), a)
		return err
	}
	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?provider=antigravity", nil))
	var attempt struct{ State string }
	if start.Code != http.StatusOK || json.Unmarshal(start.Body.Bytes(), &attempt) != nil || attempt.State == "" {
		t.Fatalf("start = %d, missing OAuth state", start.Code)
	}
	t.Cleanup(func() { CancelOAuthSession(attempt.State) })
	if path, err := WriteOAuthCallbackFileForPendingSession(authDir, "antigravity", attempt.State, "invented-code", ""); err != nil || path != "memory" {
		t.Fatal("Antigravity callback was not handed off in memory")
	}
	select {
	case session := <-completed:
		if !session.Adopted || !session.Saved || session.AuthID != "antigravity-owner@example.test.json" {
			t.Fatal("completion did not report the adopted Antigravity grant")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OAuth completion hook was not called")
	}
	auth, ok := manager.GetByID("antigravity-owner@example.test.json")
	if !ok || !auth.Disabled || auth.Metadata["project_id"] != "invented-project" {
		t.Fatal("expected a staged credential and its Google project")
	}
	if entries, err := os.ReadDir(authDir); err != nil || len(entries) != 0 {
		t.Fatalf("OAuth callback directory: %d entries, %v", len(entries), err)
	}
}
