package runtime

import (
	"context"
	"os"

	accountadapters "dyna-agent/internal/account/service/adapters"
	"dyna-agent/internal/settings"
)

// piCompactionReserves uses Pi's rounded list-models window. Pi rounds to the
// nearest 100 tokens below 1M and 100000 above it. The upper bound makes the
// trigger no later than the target; the lower bound keeps the reserve safe.
func piCompactionReserves(models []accountadapters.Model, target int64) map[string]int64 {
	reserves := map[string]int64{}
	if target <= 0 {
		return reserves
	}
	for _, m := range models {
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

// piGenerationReserves discovers only the selected account's available models.
// An unavailable catalog or older Pi leaves the native defaults untouched.
func (r *Runtime) piGenerationReserves(ctx context.Context, home, account string, bundle Bundle) (map[string]int64, error) {
	v, err := settings.Load("")
	if err != nil {
		return nil, err
	}
	if v.HarnessCompactAtTokens == 0 || bundle.Executable == "" || !versionAtLeast(bundle.Version, "1.3.0") {
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
	return piCompactionReserves(models, v.HarnessCompactAtTokens), nil
}
