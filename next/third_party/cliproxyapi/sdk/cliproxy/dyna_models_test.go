package cliproxy

import (
	"context"
	"slices"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestDynaCredentialListsItsOwnModelsWithoutConfiguration(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := &coreauth.Auth{ID: "invented", Provider: "openai-compatible-custom-acme", Status: coreauth.StatusActive, Attributes: map[string]string{
		"api_key": "invented-secret", "auth_kind": "apikey", "base_url": "http://models.local/v1", "compat_name": "custom-acme", "provider_key": "openai-compatible-custom-acme",
		DynaModelsAttribute: "model-b, model-a", DynaDisplayNameAttribute: "Acme",
	}}
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatal(err)
	}
	defer GlobalModelRegistry().UnregisterClient(auth.ID)
	if err := service.ActivateDynaCredential(context.Background(), auth.ID, "model-a"); err != nil {
		t.Fatal(err)
	}
	credentials := service.DynaCredentials()
	if len(credentials) != 1 {
		t.Fatalf("credentials = %#v", credentials)
	}
	got := credentials[0]
	if !slices.Equal(got.Models, []string{"model-a", "model-b"}) || got.DisplayName != "Acme" || got.BaseURL != "http://models.local/v1" {
		t.Fatalf("credential = %#v", got)
	}
}

func TestDynaCredentialWithoutModelsHasNoCatalog(t *testing.T) {
	for _, auth := range []*coreauth.Auth{{}, {Attributes: map[string]string{DynaModelsAttribute: " , "}}} {
		if catalog := dynaCredentialCatalog(auth, "custom-acme"); catalog != nil {
			t.Fatalf("catalog = %#v", catalog)
		}
	}
}
