package folder

import "testing"

func TestPolicyDiffUsesCurrentEnginePolicy(t *testing.T) {
	desired := Folder{Policy: "front: {delete: yes}"}
	plan, err := BuildPlan(desired, Snapshot{Policy: desired.Policy})
	if err != nil || len(plan.Changes) != 0 {
		t.Fatalf("unchanged policy: %+v %v", plan, err)
	}
	plan, err = BuildPlan(desired, Snapshot{})
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Kind != PolicyKind {
		t.Fatalf("policy change: %+v %v", plan, err)
	}
}
