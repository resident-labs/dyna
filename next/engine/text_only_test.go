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
	want := []contract.TextOnlyModel{{Provider: contract.ModelProviderAnthropic, Model: "other"}, {Provider: contract.ModelProviderOpenAI, Model: "alternate"}, {Provider: contract.ModelProviderOpenAI, Model: "glm-5.3"}}
	if got := profiles.TextOnlyModels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
