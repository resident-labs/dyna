package cliproxy

import (
	"context"
	"errors"
	"slices"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// StageDynaCredential keeps a credential out of routing until it is bound.
func StageDynaCredential(_ context.Context, auth *coreauth.Auth) error {
	auth.Disabled = true
	return nil
}

// RegisterDynaCredential persists and adopts a new credential without a file.
// Its caller provides a unique ID and a fixed provider catalog.
func (s *Service) RegisterDynaCredential(ctx context.Context, auth *coreauth.Auth) error {
	_ = StageDynaCredential(ctx, auth)
	_, err := s.coreManager.Register(coreauth.WithAuthCreationIntent(ctx), auth)
	return err
}

// ActivateDynaCredential publishes a credential only after its account is bound.
func (s *Service) ActivateDynaCredential(ctx context.Context, id, model string) error {
	auth, ok, err := s.coreManager.Modify(ctx, id, 0, func(auth *coreauth.Auth) error {
		auth.Disabled = false
		if auth.Status == coreauth.StatusDisabled {
			auth.Status = coreauth.StatusActive
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("credential unavailable")
	}
	s.ensureExecutorsForAuthWithContext(ctx, auth, false)
	s.completeModelRegistrationForAuth(ctx, auth)
	if model != "" && !GlobalModelRegistry().ClientSupportsModel(id, model) {
		return errors.New("credential model registration failed")
	}
	return nil
}

// DynaAuthState is the secret-free state of a durable credential.
type DynaAuthState string

const (
	DynaAuthMissing   DynaAuthState = "missing"
	DynaAuthExpired   DynaAuthState = "sign_in_expired"
	DynaAuthFailed    DynaAuthState = "failed"
	DynaAuthConnected DynaAuthState = "connected"
	DynaAuthUnknown   DynaAuthState = "unknown"
)

// DynaCredential describes a credential without its secret or provider identity.
type DynaCredential struct {
	ID       string        `json:"id"`
	Index    string        `json:"auth_index"`
	Provider string        `json:"provider"`
	State    DynaAuthState `json:"state"`
	// DisplayName and BaseURL describe a provider the owner added. Models come
	// from the registry, so they name what the proxy routes for this credential.
	DisplayName string   `json:"display_name,omitempty"`
	BaseURL     string   `json:"base_url,omitempty"`
	Models      []string `json:"models,omitempty"`
}

// DynaCredentials lists runtime credentials, including durable pathless records.
func (s *Service) DynaCredentials() []DynaCredential {
	auths := s.coreManager.List()
	out := make([]DynaCredential, 0, len(auths))
	for _, auth := range auths {
		auth.EnsureIndex()
		credential := DynaCredential{ID: auth.ID, Index: auth.Index, Provider: auth.Provider, State: dynaCredentialState(auth)}
		if name := auth.Attributes[DynaDisplayNameAttribute]; name != "" {
			credential.DisplayName, credential.BaseURL = name, auth.Attributes["base_url"]
		}
		for _, model := range GlobalModelRegistry().GetModelsForClient(auth.ID) {
			credential.Models = append(credential.Models, model.ID)
		}
		slices.Sort(credential.Models)
		out = append(out, credential)
	}
	return out
}

func dynaCredentialState(auth *coreauth.Auth) DynaAuthState {
	if auth.RequiresSignIn {
		return DynaAuthExpired
	}
	if auth.Status == coreauth.StatusError && (auth.RefreshError != nil || auth.Failed > 0) {
		return DynaAuthFailed
	}
	if auth.InferenceVerified || auth.RenewalVerified {
		return DynaAuthConnected
	}
	return DynaAuthUnknown
}

// DeleteDynaCredential removes a durable credential and its runtime registration.
func (s *Service) DeleteDynaCredential(ctx context.Context, id string) error {
	if err := sdkauth.GetTokenStore().Delete(ctx, id); err != nil {
		return err
	}
	s.coreManager.Remove(ctx, id)
	GlobalModelRegistry().UnregisterClient(id)
	return nil
}
