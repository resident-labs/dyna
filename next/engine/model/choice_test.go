package model

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestNamespaceRejectsModelsOutsideCurrentCatalog(t *testing.T) {
	p := &Proxy{planModels: testPlanModels(), keyProviders: []KeyProvider{{Provider: "example", Models: []contract.CatalogModel{{ID: "current"}}}}}
	for _, tc := range []struct {
		choice  contract.ModelChoice
		allowed []string
		want    string
		valid   bool
	}{
		{contract.ModelChoice{Provider: "anthropic", ID: "model"}, []string{"model"}, "plan_anthropic", true},
		{contract.ModelChoice{Provider: "example", ID: "current"}, []string{"current"}, "example", true},
		{contract.ModelChoice{Provider: "example", ID: "current"}, []string{"current", "removed"}, "", false},
		{contract.ModelChoice{Provider: "example", ID: "current", Billing: contract.ModelBillingPlan}, []string{"current"}, "", false},
		{contract.ModelChoice{Provider: "missing", ID: "model"}, []string{"model"}, "", false},
	} {
		got, err := p.Namespace(tc.choice, tc.allowed)
		if (err == nil) != tc.valid || got != tc.want {
			t.Errorf("%+v allowed=%v: namespace=%q err=%v", tc.choice, tc.allowed, got, err)
		}
	}
}
