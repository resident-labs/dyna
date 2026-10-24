package adapters

import (
	"context"
	"reflect"
	"testing"
)

func TestKimiCodeModelsReadManagedCatalog(t *testing.T) {
	command := claudeFixtureCommand(t, `test "$*" = 'provider list --json'
printf '%s' '{"providers":{"managed:kimi-code":{"type":"kimi","api_key":"fixture-private"}},"models":{"kimi-code/k3":{"provider":"managed:kimi-code","model":"k3","displayName":"K3","maxContextSize":1048576,"capabilities":["thinking","always_thinking"],"supportEfforts":["low","high","max"],"defaultEffort":"max"},"kimi-code/kimi-for-coding":{"provider":"managed:kimi-code","model":"kimi-for-coding","maxContextSize":262144,"capabilities":["thinking","always_thinking"]}}}'
`)
	models, err := modelsKimiCode(context.Background(), command, nil)
	if err != nil || len(models) != 2 {
		t.Fatalf("models = %+v, %v", models, err)
	}
	first := models[0]
	if first.ID != "kimi-code/k3" || first.Name != "K3" || first.Provider != "managed:kimi-code" || first.Context != 1048576 || first.Source != "kimi provider list --json" {
		t.Fatalf("first model = %+v", first)
	}
	wantEfforts := []Effort{
		{ID: "low", Model: "kimi-code/k3#low"},
		{ID: "high", Model: "kimi-code/k3#high"},
		{ID: "max", Model: "kimi-code/k3#max", Default: true},
	}
	if !reflect.DeepEqual(first.Efforts, wantEfforts) {
		t.Fatalf("efforts = %+v, want %+v", first.Efforts, wantEfforts)
	}
	second := models[1]
	if second.ID != "kimi-code/kimi-for-coding" || second.Context != 262144 || len(second.Efforts) != 0 {
		t.Fatalf("second model = %+v", second)
	}
}
