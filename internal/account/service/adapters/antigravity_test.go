package adapters

import (
	"context"
	"testing"
)

func TestAntigravityModelsNativeTabRows(t *testing.T) {
	command := claudeFixtureCommand(t, "test \"$*\" = models\nprintf '%s\\n' 'gemini-high\tGemini High' 'gemini-high\tDuplicate' 'bad id\tBad' 'opus-low\tOpus Low'\n")
	models, err := modelsAntigravity(context.Background(), command, nil)
	if err != nil || len(models) != 2 || models[1].ID != "opus-low" {
		t.Fatalf("unexpected models: %+v %v", models, err)
	}
}
