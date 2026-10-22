package runtime

import (
	"context"

	"dyna-agent/internal/harness/config"
)

func (a *Runtime) HarnessConfig() (config.ConfigSnapshot, error) {
	return a.Config.Catalog("")
}

func (a *Runtime) AddHarnessMCP(ctx context.Context, entry config.ManagedMCP) (config.ConfigSnapshot, error) {
	m := a.Config
	if err := m.AddMCP(ctx, entry); err != nil {
		return config.ConfigSnapshot{}, err
	}
	return a.regenerateHarnessCatalog("")
}
func (a *Runtime) AddHarnessSkill(ctx context.Context, entry config.ManagedSkill) (config.ConfigSnapshot, error) {
	m := a.Config
	if err := m.AddSkill(ctx, entry); err != nil {
		return config.ConfigSnapshot{}, err
	}
	return a.regenerateHarnessCatalog("")
}
func (a *Runtime) RemoveHarnessConfig(ctx context.Context, kind, name, harness string) (config.ConfigSnapshot, error) {
	m := a.Config
	if err := m.Remove(ctx, kind, name, harness); err != nil {
		return config.ConfigSnapshot{}, err
	}
	return a.regenerateHarnessCatalog(harness)
}
func (a *Runtime) regenerateHarnessCatalog(harness string) (config.ConfigSnapshot, error) {
	if err := a.Regenerate(harness); err != nil {
		return config.ConfigSnapshot{}, err
	}
	return a.Config.Catalog("")
}
