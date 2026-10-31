package cliproxy

import (
	"context"
	"errors"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// RegisterDynaCredential persists and adopts a new credential without a file.
// Its caller provides a unique ID and a fixed provider catalog.
func (s *Service) RegisterDynaCredential(ctx context.Context, auth *coreauth.Auth, model string) error {
	registered, err := s.coreManager.Register(coreauth.WithAuthCreationIntent(ctx), auth)
	if err != nil {
		return err
	}
	s.ensureExecutorsForAuthWithContext(ctx, registered, false)
	s.completeModelRegistrationForAuth(ctx, registered)
	if !GlobalModelRegistry().ClientSupportsModel(registered.ID, model) {
		_ = s.DeleteDynaCredential(ctx, registered.ID)
		return errors.New("credential model registration failed")
	}
	return nil
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
