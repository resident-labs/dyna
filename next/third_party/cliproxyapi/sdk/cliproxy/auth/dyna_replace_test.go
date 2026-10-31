package auth

import (
	"context"
	"errors"
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

type rejectedGrantStore struct{ *memoryAuthTestStore }

func (s *rejectedGrantStore) Save(ctx context.Context, auth *Auth) (string, error) {
	if HasAuthCreationIntent(ctx) {
		return "", errors.New("save failed")
	}
	return s.memoryAuthTestStore.Save(ctx, auth)
}

func TestReplaceDoesNotPublishFailedSave(t *testing.T) {
	ctx := context.Background()
	st := &rejectedGrantStore{newMemoryAuthTestStore()}
	m := NewManager(st, nil, nil)
	old := &Auth{ID: "credential", Provider: "kimi", Metadata: map[string]any{"access_token": "old"}}
	if _, err := m.Register(ctx, old); err != nil {
		t.Fatal(err)
	}
	_, err := m.Replace(ctx, &Auth{ID: old.ID, Provider: old.Provider, Metadata: map[string]any{"access_token": "new"}})
	if err == nil {
		t.Fatal("accepted a failed grant save")
	}
	current, ok := m.GetByID(old.ID)
	if !ok || current.Metadata["access_token"] != "old" {
		t.Fatal("published an unsaved grant")
	}
}

type blockedGrantStore struct {
	*memoryAuthTestStore
	entered chan struct{}
	release chan struct{}
}

func (s *blockedGrantStore) Save(ctx context.Context, auth *Auth) (string, error) {
	if HasAuthCreationIntent(ctx) {
		close(s.entered)
		<-s.release
	}
	return s.memoryAuthTestStore.Save(ctx, auth)
}

func TestRemoveWaitsForReplace(t *testing.T) {
	ctx := context.Background()
	st := &blockedGrantStore{memoryAuthTestStore: newMemoryAuthTestStore(), entered: make(chan struct{}), release: make(chan struct{})}
	m := NewManager(st, nil, nil)
	old := &Auth{ID: "credential", Provider: "kimi", Metadata: map[string]any{"access_token": "old"}}
	if _, err := m.Register(ctx, old); err != nil {
		t.Fatal(err)
	}
	replaced := make(chan error, 1)
	go func() {
		_, err := m.Replace(ctx, &Auth{ID: old.ID, Provider: old.Provider, Metadata: map[string]any{"access_token": "new"}})
		replaced <- err
	}()
	<-st.entered
	removed := make(chan struct{})
	go func() { m.Remove(ctx, old.ID); close(removed) }()
	close(st.release)
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	<-removed
	if _, ok := m.GetByID(old.ID); ok {
		t.Fatal("removed grant remains in runtime")
	}
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
	if err := <-done; err == nil {
		t.Fatal("stale refresh was not rejected")
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
