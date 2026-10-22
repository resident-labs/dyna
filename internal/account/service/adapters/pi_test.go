package adapters

import (
	"context"
	"reflect"
	"testing"
)

// The table shape is copied from Pi 1.3.0 `pi --list-models`.
func TestPiModelsReadListTable(t *testing.T) {
	command := claudeFixtureCommand(t, "test \"$*\" = '--list-models'\nprintf '%s\\n' 'provider   model            context  max-out  thinking  images' 'anthropic  claude-fable-5   1M       128K     yes       yes' 'openai     gpt-small        200K     16.4K    no        no' 'openai     gpt-small        200K     16.4K    no        no'\n")
	models, err := modelsPi(context.Background(), command, nil)
	if err != nil || len(models) != 2 {
		t.Fatalf("models = %+v, %v", models, err)
	}
	first := models[0]
	if first.ID != "anthropic/claude-fable-5" || first.Provider != "anthropic" || first.Context != 1000000 || first.Output != 128000 || len(first.Efforts) != len(piThinking) {
		t.Fatalf("first = %+v", first)
	}
	if !reflect.DeepEqual(first.Efforts[3], Effort{ID: "medium", Args: []string{"--thinking", "medium"}}) {
		t.Fatalf("effort = %+v", first.Efforts[3])
	}
	if models[1].Output != 16400 || models[1].Efforts != nil {
		t.Fatalf("second = %+v", models[1])
	}
	empty := claudeFixtureCommand(t, "printf '%s\\n' 'No models available. Use /login to log into a provider via OAuth or API key.'\n")
	if models, err := modelsPi(context.Background(), empty, nil); err == nil || len(models) != 0 {
		t.Fatalf("invented models: %+v", models)
	}
}
