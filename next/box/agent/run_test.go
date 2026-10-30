package agent

import (
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestModelArgsUseConfiguredProviderAndThinking(t *testing.T) {
	args, err := modelArgs(contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "model-a", Thinking: contract.ThinkingHigh})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--provider", "openai", "--model", "model-a", "--thinking", "high"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v", args)
	}
}
