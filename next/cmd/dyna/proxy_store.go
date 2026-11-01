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

func keyProviderURL(proxyProvider string) (string, bool) {
	for _, provider := range model.KeyProviders() {
		if proxyProvider == "openai-compatible-"+string(provider.Provider) {
			return provider.URL, true
		}
	}
	return "", false
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
		// A key record keeps the URL it was created with, and the executor sends to
		// that URL. The catalog decides, so a corrected endpoint reaches old accounts.
		if url, ok := keyProviderURL(record.Provider); ok && auth.Attributes != nil {
			auth.Attributes["base_url"] = url
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
	var attempt string
	var identity model.OAuthIdentity
	if info := proxyauth.GetRequestInfo(ctx); info != nil {
		attempt = info.Headers.Get("X-Dyna-Attempt")
		if raw := info.OAuthIdentity; raw != nil {
			identity = model.OAuthIdentity{AccountID: raw.AccountID, AccountUUID: raw.AccountUUID, Subject: raw.Subject, Email: raw.Email}
		}
	}
	req := model.AuthSave{Record: store.ProxyAuth{ID: copy.ID, Provider: copy.Provider, Record: raw, UpdatedAt: time.Now().UnixMilli()}, Create: proxyauth.HasAuthCreationIntent(ctx), Attempt: attempt, Identity: identity}
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
