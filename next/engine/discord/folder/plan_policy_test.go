package folder

import (
	"strings"
	"testing"
)

func TestPolicyDiffUsesCurrentEnginePolicy(t *testing.T) {
	desired := Folder{Policy: "front: {delete: yes}"}
	plan, err := BuildPlan(desired, Snapshot{Policy: desired.Policy})
	if err != nil || len(plan.Changes) != 0 {
		t.Fatalf("unchanged policy: %+v %v", plan, err)
	}
	semantic := Folder{Policy: "log: private-log\nfront: {delete: yes, threads: no}"}
	reordered := Snapshot{Policy: "front:\n  threads: false\n  delete: true\nlog: private-log\nenabled: true\n"}
	same, err := BuildPlan(semantic, reordered)
	if err != nil || len(same.Changes) != 0 {
		t.Fatalf("semantic policy diff: %s %v", same.String(), err)
	}
	plan, err = BuildPlan(desired, Snapshot{})
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Kind != PolicyKind {
		t.Fatalf("policy change: %+v %v", plan, err)
	}
	if strings.Contains(plan.String(), "delete:") || !strings.Contains(plan.String(), "policy.yaml differs from the enabled policy.") {
		t.Fatal(plan.String())
	}
}
