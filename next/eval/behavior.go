package eval

import (
	"context"
	"fmt"
)

// Change is a fixed behavior-script transition.
type Change string

const (
	InstructionAdd    Change = "instruction_add"
	InstructionRemove Change = "instruction_remove"
	ChildFinishes     Change = "child_finishes"
	UsagePace         Change = "usage_pace"
	ToolSearch        Change = "tool_search"
	SkillAdd          Change = "skill_add"
	Compaction        Change = "compaction"
)

var behaviorChanges = [...]Change{InstructionAdd, InstructionRemove, ChildFinishes, UsagePace, ToolSearch, SkillAdd, Compaction}

// ProbePrompt is identical for each behavior step and each arm.
const ProbePrompt = "Report the current relevant context and use the available tools if needed."

// BehaviorObservation contains only captured evidence; the runner must not
// infer missing usage, compaction joins or whether a change reached Pi.
type BehaviorObservation struct {
	Change           Change
	Applicable       bool
	EvidenceComplete bool
	UpdateObserved   bool
	Calls            []Call
	UsageAvailable   bool
	Usage            Usage
}

// BehaviorBackend runs one continuation per provider and arm after all gate
// tasks. It journals each step before changing state or starting the model.
type BehaviorBackend interface {
	BeginBehavior(context.Context, Provider, Arm) error
	ApplyAndProbe(context.Context, Provider, Arm, Change, string) (BehaviorObservation, error)
	EndBehavior(context.Context, Provider, Arm) error
}

// RunBehavior runs all seven changes in the settled order. The backend must
// refuse replays and must report inapplicable or missing evidence explicitly.
func RunBehavior(ctx context.Context, backend BehaviorBackend, emit func(Provider, Arm, BehaviorObservation) error) error {
	providers := [...]Provider{Opus, Sonnet, ChatGPT, Zai, Kimi}
	arms := [...]Arm{Off, Caller, Declared}
	for _, provider := range providers {
		for _, arm := range arms {
			if err := backend.BeginBehavior(ctx, provider, arm); err != nil {
				return err
			}
			for _, change := range behaviorChanges {
				result, err := backend.ApplyAndProbe(ctx, provider, arm, change, ProbePrompt)
				if err != nil {
					return fmt.Errorf("%s %s %s: %w", provider, arm, change, err)
				}
				if result.Change != change {
					return fmt.Errorf("behavior result does not match %s", change)
				}
				if err := emit(provider, arm, result); err != nil {
					return err
				}
			}
			if err := backend.EndBehavior(ctx, provider, arm); err != nil {
				return err
			}
		}
	}
	return nil
}
