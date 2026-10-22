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

// AddCatalogSkill keeps the CLI refresh scope separate from entry enablement.
func (r *Runtime) AddCatalogSkill(ctx context.Context, entry config.ManagedSkill, refreshHarness string) error {
	if err := r.Config.AddSkill(ctx, entry); err != nil {
		return err
	}
	return r.refreshSavedCatalog(refreshHarness)
}

func (r *Runtime) RemoveCatalogEntry(ctx context.Context, kind, name, harness string) error {
	if err := r.Config.Remove(ctx, kind, name, harness); err != nil {
		return err
	}
	return r.refreshSavedCatalog(harness)
}

func (r *Runtime) SetCatalogEntryEnabled(ctx context.Context, kind, name, harness string, enabled bool) error {
	if err := r.Config.SetEnabled(ctx, kind, name, harness, enabled); err != nil {
		return err
	}
	return r.refreshSavedCatalog(harness)
}

func (r *Runtime) refreshSavedCatalog(harness string) error {
	if err := r.Regenerate(harness); err != nil {
		return fmt.Errorf("catalog saved; private generation is pending: %w", err)
	}
	return nil
}
