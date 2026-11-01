package management

import (
	"context"
	"testing"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestDynaMetaOAuthCompletesAfterAdoption(t *testing.T) {
	previousEnabled := DynaMemoryOAuthCallbacks
	defer func() { DynaMemoryOAuthCallbacks = previousEnabled }()
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
	const state = "invented-meta-state"
	RegisterOAuthSession(state, "meta")
	t.Cleanup(func() { oauthSessions.mu.Lock(); delete(oauthSessions.sessions, state); oauthSessions.mu.Unlock() })
	bundle := &metaauth.MetaAuthBundle{
		TokenData: &metaauth.TokenData{AccessToken: "dca:invented", TokenType: "Bearer"},
		MintedKey: &metaauth.MintedKeyResponse{APIKey: "LLM|invented", UserEmail: "owner@example.com"},
	}
	record := buildMetaAuthRecord(bundle, metaauth.NewMetaAuth(nil).CreateTokenStorage(bundle))
	if err := claimOAuthSessionCommit(state, "meta"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.saveTokenRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	h.dynaCompleteOAuth(state, record)
	if session, ok := oauthSessions.Get(state); !ok || !session.Adopted {
		t.Fatal("a saved Meta sign-in was not adopted")
	}
}
