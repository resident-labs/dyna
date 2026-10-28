package adapters

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The fixture is the output shape of `pi --list-models --json` (fork level 2).
func TestPiModelsDecodeListJSON(t *testing.T) {
	catalog := `[` +
		`{"provider":"openai-codex","id":"gpt-6.1-terra","name":"Terra","contextWindow":483616,"maxTokens":128000,"reasoning":true,"thinkingLevels":["off","low","medium","high"]},` +
		`{"provider":"anthropic","id":"claude-fable-5","name":"Claude Fable 5","contextWindow":200000,"maxTokens":64000,"reasoning":false,"thinkingLevels":["off"]}` +
		`]`
	command := claudeFixtureCommand(t, "test \"$*\" = '--list-models --json'\nprintf '%s\\n' "+strconv.Quote(catalog)+"\n")
	models, err := modelsPi(context.Background(), command, nil)
	if err != nil || len(models) != 2 {
		t.Fatalf("models = %+v, %v", models, err)
	}
	first := models[0]
	if first.ID != "openai-codex/gpt-6.1-terra" || first.Provider != "openai-codex" || first.Name != "Terra" || first.Context != 483616 || first.Output != 128000 {
		t.Fatalf("first = %+v", first)
	}
	if len(first.Efforts) != 4 || !reflect.DeepEqual(first.Efforts[2], Effort{ID: "medium", Args: []string{"--thinking", "medium"}}) {
		t.Fatalf("efforts = %+v", first.Efforts)
	}
	if len(models[1].Efforts) != 1 || models[1].Efforts[0].ID != "off" {
		t.Fatalf("second efforts = %+v", models[1].Efforts)
	}
	empty := claudeFixtureCommand(t, "printf '%s\\n' '[]'\n")
	if models, err := modelsPi(context.Background(), empty, nil); err == nil || len(models) != 0 {
		t.Fatalf("invented models: %+v, %v", models, err)
	}
	broken := claudeFixtureCommand(t, "printf '%s\\n' 'provider model context'\n")
	if _, err := modelsPi(context.Background(), broken, nil); err == nil || !strings.Contains(err.Error(), "catalog is unavailable") {
		t.Fatalf("text table accepted as JSON: %v", err)
	}
}
