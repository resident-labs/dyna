package agent_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"
)

func TestUncataloguedModelTakesImages(t *testing.T) {
	models := agent.Models(contract.TextOnlyModel{Provider: contract.ModelProviderAnthropic, Model: "claude-fable-5-2", Images: true})
	got := models.Providers["anthropic"].Models
	if len(got) != 1 || got[0].ID != "claude-fable-5-2" || !reflect.DeepEqual(got[0].Input, []string{"text", "image"}) || got[0].API != "anthropic-messages" {
		t.Fatalf("models = %+v", got)
	}
}

func TestTextOnlyModels(t *testing.T) {
	entries := []contract.TextOnlyModel{{Provider: contract.ModelProviderOpenAI, Model: "glm-5.3"}, {Provider: contract.ModelProviderOpenAI, Model: "alternate"}, {Provider: contract.ModelProviderAnthropic, Model: "other"}}
	models := agent.Models(entries...)
	for _, tc := range []struct {
		provider string
		ids      []string
	}{{"openai", []string{"glm-5.3", "alternate"}}, {"anthropic", []string{"other"}}} {
		got := models.Providers[tc.provider].Models
		if len(got) != len(tc.ids) {
			t.Fatalf("%s: %+v", tc.provider, got)
		}
		for i, m := range got {
			if m.ID != tc.ids[i] || !m.Reasoning || !reflect.DeepEqual(m.Input, []string{"text"}) {
				t.Fatalf("%s: %+v", tc.provider, m)
			}
		}
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	var decoded agent.ModelsConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, models) {
		t.Fatalf("models.json: %s", encoded)
	}
	worker := agent.WorkerModels("worker", entries...)
	for _, provider := range []string{"anthropic", "openai"} {
		if worker.Providers[provider].Headers[contract.WorkerHeader] != "worker" || !reflect.DeepEqual(worker.Providers[provider].Models, models.Providers[provider].Models) {
			t.Fatalf("worker %s: %+v", provider, worker.Providers[provider])
		}
	}
}
