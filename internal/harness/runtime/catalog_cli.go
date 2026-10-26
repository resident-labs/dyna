package runtime

import (
	"context"
	"fmt"

	"dyna-agent/internal/harness/config"
)

// AddCatalogMCP keeps the CLI refresh scope separate from entry enablement.
func (r *Runtime) AddCatalogMCP(ctx context.Context, entry config.ManagedMCP, refreshHarness string) error {
	if err := r.Config.AddMCP(ctx, entry); err != nil {
		return err
	}
	return r.refreshSavedCatalog(refreshHarness)
}

// AddCatalogSkill saves a skill. The skill catalog is global, so the edit never
// regenerates a native config; enabled skills reach sessions through their
// standing instructions.
func (r *Runtime) AddCatalogSkill(ctx context.Context, entry config.ManagedSkill, _ string) error {
	return r.Config.AddSkill(ctx, entry)
}

func (r *Runtime) RemoveCatalogEntry(ctx context.Context, kind, name, harness string) error {
	if err := r.Config.Remove(ctx, kind, name, harness); err != nil {
		return err
	}
	if kind != "mcp" {
		return nil
	}
	return r.refreshSavedCatalog(harness)
}

func (r *Runtime) SetCatalogEntryEnabled(ctx context.Context, kind, name, harness string, enabled bool) error {
	if err := r.Config.SetEnabled(ctx, kind, name, harness, enabled); err != nil {
		return err
	}
	if kind != "mcp" {
		return nil
	}
	return r.refreshSavedCatalog(harness)
}

func (r *Runtime) refreshSavedCatalog(harness string) error {
	if err := r.Regenerate(harness); err != nil {
		return fmt.Errorf("catalog saved; private generation is pending: %w", err)
	}
	return nil
}
