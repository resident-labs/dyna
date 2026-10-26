package runtime

import (
	"context"
	"errors"

	"dyna-agent/internal/authority"
)

// ConfigSyncResult is the user-visible generation result. Launch environment
// values and arguments stay inside the engine.
type ConfigSyncResult struct {
	ID             string   `json:"id"`
	Directory      string   `json:"directory"`
	PartialReasons []string `json:"partialReasons,omitempty"`
}

// SyncCatalogAccount publishes MCP config for the explicitly selected account.
// Automatic generation keeps using RegenerateAccount without a user rule. A
// skill never enters a native config, so the skill catalog has no sync.
func (r *Runtime) SyncCatalogAccount(ctx context.Context, kind, harness, accountID string) (ConfigSyncResult, error) {
	if kind != "mcp" && kind != "skill" {
		return ConfigSyncResult{}, errors.New("unsupported config kind")
	}
	if err := authority.Check("mcp sync", authority.Of(ctx), nil); err != nil {
		return ConfigSyncResult{}, err
	}
	if kind != "mcp" {
		return ConfigSyncResult{}, errors.New("only MCP config syncs into a private harness home; enabled skills reach sessions through their standing instructions")
	}
	if accountID == "" {
		return ConfigSyncResult{}, errors.New("sync requires --account with an explicit private account ID")
	}
	if harness == "" {
		return ConfigSyncResult{}, errors.New("sync requires --harness and a private account scope")
	}
	generation, err := r.RegenerateAccount(harness, accountID)
	if err != nil {
		return ConfigSyncResult{}, err
	}
	return ConfigSyncResult{ID: generation.ID, Directory: generation.Directory, PartialReasons: generation.PartialReasons}, nil
}
