package runtime

import (
	"context"
	"os"

	accountadapters "dyna-agent/internal/account/service/adapters"
	"dyna-agent/internal/profile"
)

// piCompactionReserves uses Pi's rounded list-models window. Pi rounds to the
// nearest 100 tokens below 1M and 100000 above it. The upper bound makes the
// trigger no later than the target; the lower bound keeps the reserve safe.
func piCompactionReserves(models []accountadapters.Model, targets map[string]int64) map[string]int64 {
	reserves := map[string]int64{}
	for _, m := range models {
		target := targets[m.ID]
		if target <= 0 {
			continue
		}
		window := m.Context
		if window <= 16384 || window > 9007199254690991 || window-16384 <= target {
			continue
		}
		margin := int64(50)
		if window >= 1000000 {
			margin = 50000
		}
		reserve := window + margin - target
		if reserve < 16384 {
			reserve = 16384
		}
		if reserve > window-margin {
			reserve = window - margin
		}
		reserves[m.ID] = reserve
	}
	return reserves
}

// piProfileTargets shares the smallest target between profiles using a model.
func piProfileTargets(profiles []profile.Profile) map[string]int64 {
	targets := map[string]int64{}
	for _, p := range profiles {
		if p.Harness != profile.HarnessPi || p.Compact == nil {
			continue
		}
		for _, model := range p.EffectiveModels() {
			if previous := targets[model]; previous == 0 || p.Compact.Tokens < previous {
				targets[model] = p.Compact.Tokens
			}
		}
	}
	return targets
}

// piGenerationReserves discovers only the selected account's available models.
// An unavailable catalog leaves the native defaults untouched.
func (r *Runtime) piGenerationReserves(ctx context.Context, home, account string, bundle Bundle) (map[string]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	registry, err := profile.ReadConfig(profile.DefaultPath())
	if err != nil {
		return nil, err
	}
	targets := piProfileTargets(registry.Store.Profiles)
	if len(targets) == 0 || bundle.Executable == "" {
		return nil, nil
	}
	n, err := r.Accounts.Find(account)
	if err != nil {
		return nil, err
	}
	if err := copyPiModels(home, n.Home); err != nil {
		return nil, err
	}
	env := NativeEnvironmentBase("pi", os.Environ())
	for key, value := range piNativeEnv(n.Home) {
		env = append(env, key+"="+value)
	}
	models, err := accountadapters.Models(ctx, "pi", bundle.Executable, env)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	return piCompactionReserves(models, targets), nil
}
