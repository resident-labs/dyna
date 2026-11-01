package model

import (
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

// Namespace validates a profile against its provider and billing pool.
func (p *Proxy) Namespace(choice contract.ModelChoice, allowed []string) (string, error) {
	namespace := string(choice.Provider)
	billing := choice.Billing
	if billing == "" {
		if _, ok := p.pools["plan_"+namespace]; ok {
			billing = contract.ModelBillingPlan
		} else {
			billing = contract.ModelBillingKey
		}
	}
	if billing == contract.ModelBillingPlan {
		namespace = "plan_" + namespace
	} else if billing != contract.ModelBillingKey {
		return "", fmt.Errorf("provider %q does not support billing %q", choice.Provider, billing)
	}
	entry, ok := p.pools[namespace]
	if !ok {
		return "", fmt.Errorf("provider %q does not support billing %q", choice.Provider, billing)
	}
	for _, id := range append([]string{choice.ID}, allowed...) {
		if !entry.models[id] {
			return "", fmt.Errorf("model %q is not in provider %q catalog", id, choice.Provider)
		}
	}
	return namespace, nil
}
