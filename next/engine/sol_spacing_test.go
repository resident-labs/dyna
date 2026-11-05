package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type spacingProfiles struct {
	fakeProfiles
	model contract.ModelChoice
}

func (p spacingProfiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return p.model, nil, nil
}

func TestSolSpacingUsesTheResolvedModelEverywhere(t *testing.T) {
	for _, tc := range []struct {
		provider contract.ModelProvider
		id       string
		want     bool
	}{
		{contract.ModelProviderOpenAI, "gpt-6.1-sol", true},
		{contract.ModelProviderOpenAI, "gpt-6.1", true},
		{contract.ModelProviderOpenAI, "gpt-6.1-terra", true},
		{contract.ModelProviderOpenAI, "gpt-6.1-mini", true},
		{contract.ModelProviderOpenAI, "gpt-6.10-sol", false},
		{contract.ModelProviderOpenAI, "gpt-6.2-sol", false},
		{contract.ModelProviderOpenAI, "sol", false},
		{contract.ModelProviderAnthropic, "gpt-6.1-sol", false},
		{contract.ModelProviderAnthropic, "claude-sonnet-5-5", false},
	} {
		t.Run(string(tc.provider)+"/"+tc.id, func(t *testing.T) {
			s, room, sid, ctx := setup(t)
			s.profiles = spacingProfiles{model: contract.ModelChoice{Provider: tc.provider, ID: tc.id}}
			hello, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{})
			if problem != nil {
				t.Fatal(problem)
			}
			workflow, err := s.boxWorkflow(ctx, store.WorkflowRun{Room: room, Profiles: []contract.ProfileName{"test"}}, 0)
			if err != nil {
				t.Fatal(err)
			}
			turn := nextTurn(t, s, ctx, sid)
			for name, got := range map[string]string{"hello": hello.ModelInstructions, "turn": turn.ModelInstructions, "workflow": workflow.Profiles[0].ModelInstructions} {
				if (got != "") != tc.want {
					t.Errorf("%s model instructions present = %v, want %v", name, got != "", tc.want)
				}
				if tc.want {
					for _, phrase := range []string{"normal spaced prose", "numbers and IDs", "posts, briefs or files", "code and IDs themselves intact"} {
						if !strings.Contains(got, phrase) {
							t.Errorf("%s lacks %q", name, phrase)
						}
					}
				}
			}
			// The invariant prompt must not retain the model instruction after a switch.
			if strings.Contains(hello.Instructions, "normal spaced prose") || strings.Contains(hello.WorkerInstructions, "normal spaced prose") {
				t.Fatal("model guidance leaked into invariant prompt")
			}
		})
	}
}
