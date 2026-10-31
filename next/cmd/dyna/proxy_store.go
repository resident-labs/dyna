package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/rpc"
	"sync"
	"time"

	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
	proxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// proxyStore is the only Dyna component that serializes proxy credentials.
type proxyStore struct {
	mu     sync.Mutex
	client *rpc.Client
}

func (*proxyStore) RequireDurableAuth() bool { return true }

func (s *proxyStore) bound(id string) (bool, error) {
	var records []store.ProxyAuth
	if s.client.Call("Auth.List", struct{}{}, &records) != nil {
		return false, errors.New("proxy store unavailable")
	}
	for _, record := range records {
		if record.ID == id {
			return record.Bound, nil
		}
	}
	return false, nil
}

func (s *proxyStore) List(context.Context) ([]*proxyauth.Auth, error) {
	var records []store.ProxyAuth
	if err := s.client.Call("Auth.List", struct{}{}, &records); err != nil {
		return nil, errors.New("proxy store unavailable")
	}
	auths := make([]*proxyauth.Auth, 0, len(records))
	for _, record := range records {
		switch record.Provider {
		case "claude", "codex", "kimi", "xai", "openai-compatible-zai", "openai-compatible-opencode-go", "openai-compatible-deepseek":
		default:
			continue
		}
		var auth proxyauth.Auth
		if err := json.Unmarshal(record.Record, &auth); err != nil {
			return nil, errors.New("proxy store record invalid")
		}
		// Only bound credentials may be enabled after proxy restart.
		auth.Disabled = !record.Bound
		if record.Bound && auth.Status == proxyauth.StatusDisabled {
			auth.Status = proxyauth.StatusActive
		}
		auths = append(auths, &auth)
	}
	return auths, nil
}

func (s *proxyStore) Save(ctx context.Context, auth *proxyauth.Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// OAuth storage structs carry tokens outside Auth.Metadata.
	copy := auth.Clone()
	copy.Storage = nil
	if auth.Storage != nil {
		raw, err := json.Marshal(auth.Storage)
		if err != nil {
			return "", errors.New("proxy credential invalid")
		}
		// The proxy SDK defines credential metadata as map[string]any because providers use different fields.
		var metadata map[string]any
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return "", errors.New("proxy credential invalid")
		}
		for key, value := range auth.Metadata {
			metadata[key] = value
		}
		if _, ok := metadata["type"]; !ok || metadata["type"] == "" {
			metadata["type"] = auth.Provider
		}
		copy.Metadata = metadata
	}
	if auth.CodexRefreshPatch != nil || (proxyauth.IsCodexAuthRecord(auth) && !proxyauth.HasAuthCreationIntent(ctx)) {
		auths, err := s.List(ctx)
		if err != nil {
			return "", err
		}
		found := false
		for _, existing := range auths {
			if existing.ID == auth.ID {
				if auth.CodexRefreshPatch != nil {
					copy.Metadata = auth.CodexRefreshPatch.Apply(existing.Metadata)
				} else {
					copy.Metadata = proxyauth.MergeCodexSavedCredentials(existing.Metadata, copy.Metadata)
				}
				found = true
				break
			}
		}
		if !found {
			return "", errors.New("proxy credential missing")
		}
	}
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", errors.New("proxy credential invalid")
	}
	req := model.AuthSave{Record: store.ProxyAuth{ID: copy.ID, Provider: copy.Provider, Record: raw, UpdatedAt: time.Now().UnixMilli()}, Create: proxyauth.HasAuthCreationIntent(ctx)}
	if err := s.client.Call("Auth.Save", req, &struct{}{}); err != nil {
		return "", errors.New("proxy store unavailable")
	}
	auth.Metadata = copy.Metadata
	if auth.CodexRefreshPatch != nil {
		auth.CodexRefreshPatch = nil
		auth.Storage = nil
	}
	return copy.ID, nil
}

func (s *proxyStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.client.Call("Auth.Delete", model.AuthDelete{ID: id}, &struct{}{}); err != nil {
		return errors.New("proxy store unavailable")
	}
	return nil
}
