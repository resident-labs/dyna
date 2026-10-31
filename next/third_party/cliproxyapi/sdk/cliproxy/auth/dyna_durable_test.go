package auth

import (
	"context"
	"errors"
	"testing"
)

type dynaFailingStore struct{ persistFailureStore }

func (*dynaFailingStore) RequireDurableAuth() bool { return true }

func TestDynaRefreshRejectsUndurableRotation(t *testing.T) {
	m := NewManager(&dynaFailingStore{}, nil, nil)
	auth := &Auth{ID: "invented", Provider: "claude", Metadata: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}}
	if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatal(err)
	}
	m.RegisterExecutor(&unauthorizedRefreshExecutor{id: "claude"})
	if _, err := m.ForceRefreshAuth(context.Background(), auth.ID); !errors.Is(err, ErrCredentialNotDurable) {
		t.Fatal("undurable refresh did not return the typed failure")
	}
	got, ok := m.GetByID(auth.ID)
	if !ok || got.Metadata["access_token"] != "old" {
		t.Fatal("undurable refresh was published")
	}
}
