package agent

import (
	"reflect"
	"slices"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestModelArgsUseConfiguredProviderAndThinking(t *testing.T) {
	args, err := modelArgs(contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "model-a", Thinking: contract.ThinkingHigh}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--provider", "openai", "--model", "model-a", "--thinking", "high"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v", args)
	}
	args, err = modelArgs(contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "model-a", Thinking: contract.ThinkingHigh}, 150000)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, "--compaction-trigger-tokens", "150000")
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v", args)
	}
}

func TestPiArgsTurnCodemodeOnAndExcludeBash(t *testing.T) {
	args := piArgs(nil, "dyna.js", "hello", "models.json", "sessions", "mcp.json")
	for _, pair := range [][]string{{"--tools", "+codemode"}, {"--exclude-tools", "bash"}} {
		if i := slices.Index(args, pair[0]); i < 0 || args[i+1] != pair[1] {
			t.Fatalf("missing %v in %v", pair, args)
		}
	}
}
