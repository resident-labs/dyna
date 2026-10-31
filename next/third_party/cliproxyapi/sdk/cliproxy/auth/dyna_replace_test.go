package auth

import (
	"context"
	"testing"
)

type blockedRefresh struct {
	*refreshMockExecutor
	entered chan struct{}
	release chan struct{}
}

func (e *blockedRefresh) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	close(e.entered)
	<-e.release
	out := a.Clone()
	out.Metadata["access_token"] = "old-refresh-result"
	return out, nil
}

func TestReplaceDiscardsInflightRefresh(t *testing.T) {
	ctx := context.Background()
	st := newMemoryAuthTestStore()
	m := NewManager(st, nil, nil)
	e := &blockedRefresh{refreshMockExecutor: &refreshMockExecutor{id: "kimi"}, entered: make(chan struct{}), release: make(chan struct{})}
	m.RegisterExecutor(e)
	old := &Auth{ID: "credential", Provider: "kimi", Metadata: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}}
	if _, err := m.Register(ctx, old); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.ForceRefreshAuth(ctx, old.ID); done <- err }()
	<-e.entered
	grant := &Auth{ID: old.ID, Provider: old.Provider, Metadata: map[string]any{"access_token": "new", "refresh_token": "new-refresh"}}
	if _, err := m.Replace(ctx, grant); err != nil {
		t.Fatal(err)
	}
	close(e.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	current, ok := m.GetByID(old.ID)
	if !ok || current.Metadata["access_token"] != "new" {
		t.Fatal("refresh replaced runtime grant")
	}
	rows, err := st.List(ctx)
	if err != nil || len(rows) != 1 || rows[0].Metadata["access_token"] != "new" {
		t.Fatal("refresh replaced stored grant")
	}
}
