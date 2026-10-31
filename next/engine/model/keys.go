package model

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

func (c *Connections) SetKey(ctx context.Context, req contract.AccountsSetKeyRequest) (contract.AccountsSetKeyResponse, error) {
	found := false
	for _, provider := range KeyProviders() {
		if provider.Provider == req.Provider {
			found = true
			break
		}
	}
	if !found || strings.TrimSpace(req.Key) == "" || len(req.Key) > 4096 {
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
	call, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://cliproxy/dyna/keys", bytes.NewReader(body))
	if err != nil {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	call.Header.Set("Authorization", "Bearer "+c.key)
	call.Header.Set("Content-Type", "application/json")
	bound := false
	defer func() {
		if bound {
			return
		}
		cleanup, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, "http://cliproxy/dyna/keys/"+proxyID, nil)
		if err != nil {
			return
		}
		cleanup.Header.Set("Authorization", "Bearer "+c.key)
		if response, err := c.proxy.client.Do(cleanup); err == nil {
			response.Body.Close()
		}
	}()
	generation := c.proxy.generation.Load()
	res, err := c.proxy.client.Do(call)
	if err != nil {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || generation != c.proxy.generation.Load() {
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	if err := c.store.BindAccount(ctx, accountID, proxyID, req.Provider, req.Label); err != nil {
		// An unbound key is private and is pruned at proxy startup.
		return contract.AccountsSetKeyResponse{}, errConnect
	}
	bound = true
	return contract.AccountsSetKeyResponse{AccountID: accountID}, nil
}
