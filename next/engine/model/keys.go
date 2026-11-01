package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// ErrModelsUnavailable means a provider lists no models and none were entered.
var ErrModelsUnavailable = errors.New("provider models unavailable")

// keyTarget is everything the proxy needs to hold a key for one provider.
type keyTarget struct {
	Provider    contract.AccountProvider `json:"provider"`
	DisplayName string                   `json:"displayName,omitempty"`
	BaseURL     string                   `json:"baseUrl,omitempty"`
	Models      []string                 `json:"models,omitempty"`
	// Discover asks the provider's own list to extend Models.
	Discover bool `json:"discover,omitempty"`
}

func (c *Connections) keyTarget(ctx context.Context, req contract.AccountsSetKeyRequest) (keyTarget, error) {
	if req.Provider == contract.ProviderMeta {
		return keyTarget{Provider: req.Provider, Models: []string{MetaKeyModel}}, nil
	}
	for _, preset := range KeyPresets() {
		if preset.Provider == req.Provider {
			return keyTarget{Provider: req.Provider, DisplayName: preset.DisplayName, BaseURL: preset.URL, Models: preset.Models, Discover: true}, nil
		}
	}
	credentials, err := c.management.credentials(ctx)
	if err != nil {
		return keyTarget{}, errConnect
	}
	provider := req.Provider
	if provider == "" {
		if !ValidNewProvider(req.DisplayName, req.BaseURL) {
			return keyTarget{}, errConnect
		}
		provider = customProviderID(req.DisplayName)
	}
	for _, credential := range credentials {
		if credential.Provider != proxyCompatPrefix+string(provider) {
			continue
		}
		// One name is one provider. Another key for it comes from Add a key.
		if req.Provider == "" {
			return keyTarget{}, errConnect
		}
		models := make([]string, 0, len(credential.Models))
		for _, model := range credential.Models {
			models = append(models, strings.TrimPrefix(model, string(provider)+"/"))
		}
		return keyTarget{Provider: provider, DisplayName: credential.DisplayName, BaseURL: credential.BaseURL, Models: models, Discover: true}, nil
	}
	if req.Provider != "" {
		return keyTarget{}, errConnect
	}
	models := cleanModels(req.Models)
	return keyTarget{Provider: provider, DisplayName: strings.TrimSpace(req.DisplayName), BaseURL: strings.TrimSpace(req.BaseURL), Models: models, Discover: len(models) == 0}, nil
}

func (c *Connections) SetKey(ctx context.Context, req contract.AccountsSetKeyRequest) (contract.AccountsSetKeyResponse, error) {
	if strings.TrimSpace(req.Key) == "" || len(req.Key) > 4096 {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	target, err := c.keyTarget(ctx, req)
	if err != nil {
		return contract.AccountsSetKeyResponse{}, err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	proxyID := "dyna-key-" + hex.EncodeToString(raw[:16])
	accountID := contract.AccountID(hex.EncodeToString(raw[16:]))
	body, err := json.Marshal(struct {
		ID  string `json:"id"`
		Key string `json:"key"`
		keyTarget
	}{proxyID, req.Key, target})
	if err != nil {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	bound := false
	defer func() {
		if bound {
			return
		}
		_, _ = c.management.request(context.Background(), http.MethodDelete, "/dyna/keys/"+proxyID, nil)
	}()
	generation := c.proxy.generation.Load()
	data, err := c.management.request(ctx, http.MethodPost, "/dyna/keys", body)
	if errors.Is(err, ErrModelsUnavailable) {
		return contract.AccountsSetKeyResponse{}, err
	}
	var saved struct {
		Model string `json:"model"`
	}
	if err != nil || json.Unmarshal(data, &saved) != nil || saved.Model == "" || generation != c.proxy.generation.Load() {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	if err := c.store.BindAccount(ctx, accountID, proxyID, target.Provider, req.Label); err != nil {
		// An unbound key is private and is pruned at proxy startup.
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	bound = true
	payload, err := json.Marshal(struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}{proxyID, saved.Model})
	if err != nil {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	// Finish activation or rollback even when the request is canceled after binding.
	finish := context.WithoutCancel(ctx)
	if _, err = c.management.request(finish, http.MethodPost, "/dyna/credentials/activate", payload); err != nil {
		if c.RemoveCredential(finish, proxyID) == nil {
			// The account remains removable if the store cannot complete cleanup.
			_ = c.store.RemoveAccount(finish, accountID)
		}
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	return contract.AccountsSetKeyResponse{AccountID: accountID}, nil
}
