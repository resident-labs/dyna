// Package replayreason defines the typed outcome of automatic workflow replay.
package replayreason

type WorkflowReplayReason string

const (
	Eligible         WorkflowReplayReason = "eligible"
	Legacy           WorkflowReplayReason = "legacy"
	NotDeterministic WorkflowReplayReason = "not_deterministic"
	Incomplete       WorkflowReplayReason = "incomplete"
	Uncertain        WorkflowReplayReason = "uncertain"
	Unsafe           WorkflowReplayReason = "unsafe"
	InputMismatch    WorkflowReplayReason = "input_mismatch"
	LaunchUncertain  WorkflowReplayReason = "launch_uncertain"
)

var All = []WorkflowReplayReason{Eligible, Legacy, NotDeterministic, Incomplete, Uncertain, Unsafe, InputMismatch, LaunchUncertain}

// Description explains a replay decision to the run's recipient.
func (r WorkflowReplayReason) Description() string {
	switch r {
	case Legacy:
		return "This run has no replay records."
	case NotDeterministic:
		return "The workflow author did not enable safe replay."
	case Incomplete:
		return "The replay record is incomplete."
	case Uncertain:
		return "A prior call has an unknown result."
	case Unsafe:
		return "The run contains work that cannot be replayed safely."
	case InputMismatch:
		return "The workflow inputs or worktree changed."
	case LaunchUncertain:
		return "Dyna could not confirm whether replay started."
	default:
		return ""
	}
}
