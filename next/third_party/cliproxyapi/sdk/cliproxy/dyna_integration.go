//go:build dyna_integration

package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type DynaIntegrationHarness = management.DynaIntegrationHarness

func NewDynaIntegrationHarness(store coreauth.Store, notify func(string, string)) *DynaIntegrationHarness {
	return management.NewDynaIntegrationHarness(store, notify)
}
