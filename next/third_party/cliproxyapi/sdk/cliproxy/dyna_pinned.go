package cliproxy

import "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"

// DynaPinnedPlanModels is the immutable plan model catalog in the vendored build.
type DynaPinnedPlanModels = registry.DynaPinnedPlanModels

// DynaPinnedModels never reads the updater's mutable registry.
func DynaPinnedModels() (DynaPinnedPlanModels, error) {
	return registry.DynaPinnedModels()
}
