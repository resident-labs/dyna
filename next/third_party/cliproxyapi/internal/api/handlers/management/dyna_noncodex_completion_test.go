package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type dynaDurableProviderStore struct{ dynaFakeProviderStore }

func (dynaDurableProviderStore) RequireDurableAuth() bool { return true }

func TestDynaReconnectAdoptsEnabledGrant(t *testing.T) {
	previousEnabled, previousHook := DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook
	defer func() { DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook = previousEnabled, previousHook }()
	DynaMemoryOAuthCallbacks = true
	ctx := context.Background()
	st := dynaDurableProviderStore{}
	manager := coreauth.NewManager(st, nil, nil)
	if _, err := manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: "reconnect", Provider: "kimi", Metadata: map[string]any{"access_token": "old"}}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	h.tokenStore = st
	h.postAuthHook = func(_ context.Context, a *coreauth.Auth) error { a.Disabled = true; return nil }
	const state = "invented-reconnect-state"
	RegisterOAuthSession(state, "kimi")
	t.Cleanup(func() { oauthSessions.mu.Lock(); delete(oauthSessions.sessions, state); oauthSessions.mu.Unlock() })
	accepted := false
	DynaOAuthCompletionHook = func(state, _ string) { session, _ := oauthSessions.Get(state); accepted = session.Adopted }
	record := &coreauth.Auth{ID: "reconnect", Provider: "kimi", Storage: &kimi.KimiTokenStorage{AccessToken: "new", RefreshToken: "new-refresh"}}
	if _, err := h.saveTokenRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	h.dynaCompleteOAuth(state, record)
	if !accepted {
		t.Fatal("enabled bound grant was not adopted")
	}
}

type dynaFakeProviderStore struct{}

func (dynaFakeProviderStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (dynaFakeProviderStore) Delete(context.Context, string) error           { return nil }
func (dynaFakeProviderStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	raw, err := json.Marshal(auth.Storage)
	if err != nil {
		return "", err
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return "", err
	}
	for k, v := range auth.Metadata {
		metadata[k] = v
	}
	auth.Metadata = metadata
	return auth.ID, nil
}

func TestDynaCompletionRejectsDifferentRuntimeGrant(t *testing.T) {
	previousEnabled, previousHook := DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook
	defer func() { DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook = previousEnabled, previousHook }()
	DynaMemoryOAuthCallbacks = true
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
		ID: "invented", Provider: "kimi", Disabled: true, Metadata: map[string]any{"access_token": "older-token"},
	}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	const state = "invented-mismatch-state"
	RegisterOAuthSession(state, "kimi")
	t.Cleanup(func() { oauthSessions.mu.Lock(); delete(oauthSessions.sessions, state); oauthSessions.mu.Unlock() })
	called := false
	DynaOAuthCompletionHook = func(state, _ string) {
		called = true
		session, _ := oauthSessions.Get(state)
		if session.Adopted {
			t.Error("different runtime grant was accepted")
		}
	}
	h.dynaCompleteOAuth(state, &coreauth.Auth{ID: "invented", Metadata: map[string]any{"access_token": "newer-token"}})
	if !called {
		t.Fatal("failed adoption was not reported")
	}
}

func TestDynaCompletionRejectsEnabledRuntimeGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousEnabled, previousHook := DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook
	defer func() { DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook = previousEnabled, previousHook }()
	DynaMemoryOAuthCallbacks = true
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: "invented", Provider: "kimi", Metadata: map[string]any{"access_token": "invented-token"}}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	const state = "invented-enabled-state"
	RegisterOAuthSession(state, "kimi")
	t.Cleanup(func() { oauthSessions.mu.Lock(); delete(oauthSessions.sessions, state); oauthSessions.mu.Unlock() })
	called := false
	DynaOAuthCompletionHook = func(state, _ string) {
		called = true
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/status?state="+state, nil)
		h.GetAuthStatus(c)
		var result struct {
			Adopted bool `json:"adopted"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Error(err)
		}
		if result.Adopted {
			t.Error("status promoted an enabled unbound credential")
		}
	}
	h.dynaCompleteOAuth(state, &coreauth.Auth{ID: "invented", Metadata: map[string]any{"access_token": "invented-token"}})
	if !called {
		t.Fatal("failed adoption was not reported")
	}
}

func TestDynaKimiOAuthCompletesAfterAdoption(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousEnabled, previousHook := DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook
	defer func() { DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook = previousEnabled, previousHook }()
	DynaMemoryOAuthCallbacks = true
	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	h.tokenStore = dynaFakeProviderStore{}
	h.postAuthHook = func(_ context.Context, a *coreauth.Auth) error { a.Disabled = true; return nil }
	h.postAuthPersistHook = func(ctx context.Context, a *coreauth.Auth) error {
		_, err := manager.Register(coreauth.WithSkipPersist(ctx), a)
		return err
	}
	const state = "invented-kimi-state"
	RegisterOAuthSession(state, "kimi")
	t.Cleanup(func() { oauthSessions.mu.Lock(); delete(oauthSessions.sessions, state); oauthSessions.mu.Unlock() })
	bound := false
	DynaOAuthCompletionHook = func(gotState, id string) {
		// The engine checks this status synchronously before it binds the account.
		session, ok := oauthSessions.Get(gotState)
		if !ok || !session.Adopted {
			t.Error("notification preceded actual adoption")
			return
		}
		r := httptest.NewRequest(http.MethodGet, "/status?state="+gotState, nil)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		h.GetAuthStatus(c)
		var result struct {
			Status  string `json:"status"`
			AuthID  string `json:"auth_id"`
			Saved   bool   `json:"saved"`
			Adopted bool   `json:"adopted"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Error(err)
			return
		}
		if result.Status != "ok" || result.AuthID != id || !result.Saved || !result.Adopted {
			t.Error("status did not report the adopted credential")
			return
		}
		bound = true
	}
	record := &coreauth.Auth{ID: "invented-kimi", Provider: "kimi", Storage: &kimi.KimiTokenStorage{AccessToken: "invented-token", RefreshToken: "invented-refresh"}}
	if err := claimOAuthSessionCommit(state, "kimi"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.saveTokenRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	h.dynaCompleteOAuth(state, record)
	if !bound {
		t.Fatal("unpolled Kimi completion did not bind")
	}
}
