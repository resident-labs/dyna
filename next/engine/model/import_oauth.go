package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ImportOAuth forwards the opaque credential to the proxy and binds its staged record.
func (c *Connections) ImportOAuth(ctx context.Context, req contract.AccountsImportOAuthRequest) (contract.AccountsImportOAuthResponse, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return contract.AccountsImportOAuthResponse{}, errConnect
	}
	proxyID := "dyna-oauth-" + hex.EncodeToString(raw[:16])
	accountID := contract.AccountID(hex.EncodeToString(raw[16:]))
	body, err := json.Marshal(struct {
		ID         string                   `json:"id"`
		Provider   contract.AccountProvider `json:"provider"`
		ExpiresAt  string                   `json:"expiresAt"`
		Credential string                   `json:"credential"`
	}{proxyID, req.Provider, req.ExpiresAt.UTC().Format(time.RFC3339Nano), req.Credential})
	if err != nil {
		return contract.AccountsImportOAuthResponse{}, errConnect
	}
	bound := false
	defer func() {
		if !bound {
			_, _ = c.management.request(context.Background(), http.MethodPost, "/dyna/credentials/remove", []byte(`{"id":"`+proxyID+`"}`))
		}
	}()
	generation := c.proxy.generation.Load()
	if _, err := c.management.request(ctx, http.MethodPost, "/dyna/oauth/import", body); err != nil || generation != c.proxy.generation.Load() {
		return contract.AccountsImportOAuthResponse{}, errConnect
	}
	if err := c.store.BindAccount(ctx, accountID, proxyID, req.Provider, req.Label); err != nil {
		return contract.AccountsImportOAuthResponse{}, errConnect
	}
	bound = true
	finish := context.WithoutCancel(ctx)
	payload, _ := json.Marshal(struct {
		ID string `json:"id"`
	}{proxyID})
	if _, err := c.management.request(finish, http.MethodPost, "/dyna/credentials/activate", payload); err != nil {
		if c.RemoveCredential(finish, proxyID) == nil {
			_ = c.store.RemoveAccount(finish, accountID)
		}
		return contract.AccountsImportOAuthResponse{}, errConnect
	}
	return contract.AccountsImportOAuthResponse{AccountID: accountID}, nil
}
