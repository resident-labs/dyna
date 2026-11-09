package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTextOnlyProfileModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"vision":{"provider":"openai","model":"vision","allowedModels":["vision"],"thinking":"high"},"text":{"provider":"openai","model":"glm-5.3","allowedModels":["glm-5.3","alternate","alternate"],"thinking":"high","images":false},"text2":{"provider":"anthropic","model":"other","allowedModels":["other"],"thinking":"off","images":false}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []contract.TextOnlyModel{{Provider: contract.ModelProviderAnthropic, Model: "other"}, {Provider: contract.ModelProviderOpenAI, Model: "alternate"}, {Provider: contract.ModelProviderOpenAI, Model: "glm-5.3"}, {Provider: contract.ModelProviderOpenAI, Model: "vision", Images: true}}
	if got := profiles.TextOnlyModels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// A model released after the pinned Pi, such as Claude Fable 5.2, is missing from Pi's catalog, so the box defines it, and
// a profile with images keeps them. A catalogued model is left to the catalog, and a text-only declaration wins.
func TestUncataloguedProfileModelsTakeImages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"fable":{"provider":"anthropic","model":"claude-fable-5-2","allowedModels":["claude-fable-5-2","claude-opus-5-5"],"thinking":"high"},"plain":{"provider":"anthropic","model":"text-model","allowedModels":["text-model"],"thinking":"high"},"plain-text":{"provider":"anthropic","model":"text-model","allowedModels":["text-model"],"thinking":"high","images":false}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []contract.TextOnlyModel{{Provider: contract.ModelProviderAnthropic, Model: "claude-fable-5-2", Images: true}, {Provider: contract.ModelProviderAnthropic, Model: "text-model"}}
	if got := profiles.TextOnlyModels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
