package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/rpc"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
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
		case "claude", "codex", "kimi", "xai", "meta", "antigravity", "openai-compatible-zai", "openai-compatible-opencode-go", "openai-compatible-deepseek":
		default:
			if !model.IsCustomProxyProvider(record.Provider) {
				continue
			}
		}
		var auth proxyauth.Auth
		if err := json.Unmarshal(record.Record, &auth); err != nil {
			return nil, errors.New("proxy store record invalid")
		}
		// A key record keeps the URL and models it was created with, and the executor sends to
		// that URL. The preset decides, so a corrected endpoint and a newly released model
		// reach old accounts: its models lead, and the account's own discovered ones follow.
		if preset, ok := model.PresetForProxy(record.Provider); ok && auth.Attributes != nil {
			auth.Attributes["base_url"] = preset.URL
			saved := strings.Split(auth.Attributes[cliproxy.DynaModelsAttribute], ",")
			auth.Attributes[cliproxy.DynaModelsAttribute] = strings.Join(mergeModels(preset.Models, saved), ",")
		}
		stripProxyRuntimeState(&auth)
		applyProxyRoutingPolicy(&auth)
		// Only bound credentials may be enabled after proxy restart.
		auth.Disabled = !record.Bound
		if record.Bound && auth.Status == proxyauth.StatusDisabled {
			auth.Status = proxyauth.StatusActive
		}
		auths = append(auths, &auth)
	}
	return auths, nil
}

// Apply the provider policy to both existing records and newly adopted credentials.
func applyProxyRoutingPolicy(auth *proxyauth.Auth) {
	delete(auth.Metadata, proxyauth.DynaRequestQuotaOnly)
	key, value := proxyauth.DynaRequestQuotaOnly, any(true)
	switch auth.Provider {
	case "openai-compatible-zai":
		if auth.AuthKind() != proxyauth.AuthKindAPIKey {
			return
		}
		key, value = "request_retry", 3
	case "claude":
		if auth.AuthKind() != proxyauth.AuthKindOAuth {
			return
		}
	case "codex", "kimi", "xai", "meta", "antigravity", "openai-compatible-opencode-go", "openai-compatible-deepseek":
	default:
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata[key] = value
}

// Keep the provider's observations (Meta has no usage endpoint), but never
// load or save runtime availability verdicts with a credential.
func stripProxyRuntimeState(auth *proxyauth.Auth) {
	auth.Quota = proxyauth.QuotaState{ObservedAt: auth.Quota.ObservedAt, Signals: auth.Quota.Signals}
	auth.NextRetryAfter, auth.Unavailable = time.Time{}, false
	auth.LastError, auth.StatusMessage = nil, ""
	if auth.Status == proxyauth.StatusError {
		auth.Status = proxyauth.StatusActive
	}
	for model, state := range auth.ModelStates {
		if state == nil || (state.Quota.ObservedAt.IsZero() && len(state.Quota.Signals) == 0) {
			delete(auth.ModelStates, model)
			continue
		}
		auth.ModelStates[model] = &proxyauth.ModelState{
			Status: proxyauth.StatusActive,
			Quota:  proxyauth.QuotaState{ObservedAt: state.Quota.ObservedAt, Signals: state.Quota.Signals},
		}
	}
}

func (s *proxyStore) Save(ctx context.Context, auth *proxyauth.Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// OAuth storage structs carry tokens outside Auth.Metadata.
	copy := auth.Clone()
	copy.Storage = nil
	stripProxyRuntimeState(copy)
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
	applyProxyRoutingPolicy(copy)
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
