package contract

const MethodContextSnapshot Method = "context.snapshot"

type ContextSectionKey string

const (
	ContextGoal     ContextSectionKey = "dyna_goal"
	ContextChildren ContextSectionKey = "dyna_children"
	ContextRequests ContextSectionKey = "dyna_requests"
	ContextHolds    ContextSectionKey = "dyna_holds"
	ContextProfiles ContextSectionKey = "dyna_profiles"
	ContextUsage    ContextSectionKey = "dyna_usage"
)

// ContextSnapshotRequest selects one section. An empty section requests all sections.
type ContextSnapshotRequest struct {
	Section ContextSectionKey `json:"section,omitempty"`
}
type ContextSnapshotResponse struct {
	Sections []ContextSection `json:"sections"`
}
type ContextSection struct {
	Key  ContextSectionKey `json:"key"`
	Text string            `json:"text"`
}
