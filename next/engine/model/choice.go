package model

import (
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

// Namespace validates a profile against the catalog and binds it to one pool.
func (p *Proxy) Namespace(choice contract.ModelChoice, allowed []string) (string, error) {
	provider := string(choice.Provider)
	plan := false
	for _, entry := range oauthProviders {
		if string(entry.info.ID) == provider && len(p.planModels[entry.info.ID]) > 0 {
			plan = true
			break
		}
	}
	billing := choice.Billing
	if billing == "" {
		if plan {
			billing = contract.ModelBillingPlan
		} else {
			billing = contract.ModelBillingKey
		}
	}
	if billing == contract.ModelBillingPlan && plan {
		for _, id := range append([]string{choice.ID}, allowed...) {
			found := false
			for _, model := range p.planModels[contract.AccountProvider(provider)] {
				if model.ID == id {
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("model %q is not in provider %q plan catalog", id, provider)
			}
		}
		return "plan_" + provider, nil
	}
	if billing == contract.ModelBillingKey {
		for _, entry := range p.keyProviders {
			if string(entry.Provider) != provider {
				continue
			}
			for _, id := range append([]string{choice.ID}, allowed...) {
				found := false
				for _, model := range entry.Models {
					if model.ID == id {
						found = true
						break
					}
				}
				if !found {
					return "", fmt.Errorf("model %q is not in provider %q catalog", id, provider)
				}
			}
			return provider, nil
		}
	}
	return "", fmt.Errorf("provider %q does not support billing %q", provider, billing)
}
