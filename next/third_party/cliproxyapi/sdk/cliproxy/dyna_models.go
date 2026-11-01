package cliproxy

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Dyna adds OpenAI-compatible providers while the proxy runs, so a provider's
// name and models live on its credentials, not in the configuration.
const (
	// DynaModelsAttribute lists a credential's own models, comma separated.
	DynaModelsAttribute = "dyna_models"
	// DynaDisplayNameAttribute is the name the owner gave a provider.
	DynaDisplayNameAttribute = "dyna_display_name"
)

// dynaCredentialCatalog reads a credential's own models as a compatibility entry.
func dynaCredentialCatalog(auth *coreauth.Auth, name string) *config.OpenAICompatibility {
	if auth.Attributes == nil {
		return nil
	}
	var models []config.OpenAICompatibilityModel
	for _, id := range strings.Split(auth.Attributes[DynaModelsAttribute], ",") {
		if id = strings.TrimSpace(id); id != "" {
			models = append(models, config.OpenAICompatibilityModel{Name: id, Alias: id})
		}
	}
	if len(models) == 0 {
		return nil
	}
	return &config.OpenAICompatibility{Name: name, Models: models}
}
