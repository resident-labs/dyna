package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

func (c *Connections) SetKey(ctx context.Context, req contract.AccountsSetKeyRequest) (contract.AccountsSetKeyResponse, error) {
	var model string
	for _, provider := range c.proxy.keyProviders {
		if provider.Provider == req.Provider {
			model = provider.DefaultModel
			if model == "" && len(provider.Models) > 0 {
				model = provider.Models[0].ID
			}
			break
		}
	}
	if model == "" || strings.TrimSpace(req.Key) == "" || len(req.Key) > 4096 {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	proxyID := "dyna-key-" + hex.EncodeToString(raw[:16])
	accountID := contract.AccountID(hex.EncodeToString(raw[16:]))
	body, err := json.Marshal(struct {
		ID       string                   `json:"id"`
		Provider contract.AccountProvider `json:"provider"`
		Key      string                   `json:"key"`
	}{proxyID, req.Provider, req.Key})
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
	_, err = c.management.request(ctx, http.MethodPost, "/dyna/keys", body)
	if err != nil || generation != c.proxy.generation.Load() {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	if err := c.store.BindAccount(ctx, accountID, proxyID, req.Provider, req.Label); err != nil {
		// An unbound key is private and is pruned at proxy startup.
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	bound = true
	payload, err := json.Marshal(struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Prefix string `json:"prefix"`
	}{proxyID, string(req.Provider) + "/" + model, string(req.Provider)})
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
