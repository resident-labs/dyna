package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

type CredentialState string

const (
	CredentialConnected CredentialState = "connected"
	CredentialExpired   CredentialState = "sign_in_expired"
	CredentialFailed    CredentialState = "failed"
	CredentialUnknown   CredentialState = "unknown"
)

type Credential struct {
	ID       string          `json:"id"`
	Index    string          `json:"auth_index"`
	Provider string          `json:"provider"`
	State    CredentialState `json:"state"`
}

func (m managementClient) credentials(ctx context.Context) ([]Credential, error) {
	data, err := m.request(ctx, http.MethodGet, "/dyna/credentials", nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Credentials []Credential `json:"credentials"`
	}
	if json.Unmarshal(data, &response) != nil {
		return nil, errors.New("management response invalid")
	}
	for _, credential := range response.Credentials {
		switch credential.State {
		case CredentialConnected, CredentialExpired, CredentialFailed, CredentialUnknown:
		default:
			return nil, errors.New("management response invalid")
		}
	}
	return response.Credentials, nil
}

func (c *Connections) Credentials(ctx context.Context) ([]Credential, error) {
	return c.management.credentials(ctx)
}

func (c *Connections) RemoveCredential(ctx context.Context, id string) error {
	body, err := json.Marshal(struct {
		ID string `json:"id"`
	}{id})
	if err != nil {
		return err
	}
	_, err = c.management.request(ctx, http.MethodPost, "/dyna/credentials/remove", body)
	return err
}
