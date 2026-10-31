package cliproxy

import (
	"context"
	"sync"
	"testing"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type dynaTestStore struct {
	mu    sync.Mutex
	saved bool
}

func (s *dynaTestStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (s *dynaTestStore) Save(_ context.Context, a *coreauth.Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = true
	return a.ID, nil
}
func (s *dynaTestStore) Delete(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = false
	return nil
}

func TestDynaRemovalFencesInflightRefresh(t *testing.T) {
	ctx := context.Background()
	st := &dynaTestStore{}
	original := sdkauth.GetTokenStore()
	sdkauth.RegisterTokenStore(st)
	defer sdkauth.RegisterTokenStore(original)
	manager := coreauth.NewManager(st, nil, nil)
	created, err := manager.Register(ctx, &coreauth.Auth{ID: "invented", Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{coreManager: manager}
	ready, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		base := created.Clone()
		close(ready)
		<-resume
		_, _ = manager.UpdateRefreshedAuth(ctx, base, base.Clone())
	}()
	<-ready
	if err := service.DeleteDynaCredential(ctx, "invented"); err != nil {
		t.Fatal(err)
	}
	close(resume)
	<-done
	if _, ok := manager.GetByID("invented"); ok || st.saved {
		t.Fatal("removed credential was restored by stale refresh")
	}
}
