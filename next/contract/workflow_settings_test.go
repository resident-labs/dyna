package contract

import "testing"

// Each switch's setting is spelled as its feature, and the settings list names each switch in the features' order, so the
// generated web type and the engine agree on every name.
func TestWorkflowSwitchSettingsAreTheFeatures(t *testing.T) {
	switches := []WorkflowSetting{WorkflowSettingWorktree, WorkflowSettingMerge, WorkflowSettingRun, WorkflowSettingWorkflow, WorkflowSettingPost, WorkflowSettingResume}
	if len(switches) != len(AllWorkflowFeatures) {
		t.Fatalf("%d switch settings for %d features", len(switches), len(AllWorkflowFeatures))
	}
	listed := AllWorkflowSettings[len(AllWorkflowSettings)-len(switches):]
	for i, feature := range AllWorkflowFeatures {
		if string(switches[i]) != string(feature) || listed[i].Setting != switches[i] {
			t.Errorf("switch %d: setting %q, listed %q, feature %q", i, switches[i], listed[i].Setting, feature)
		}
	}
}
