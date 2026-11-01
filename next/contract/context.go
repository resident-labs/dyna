package contract

const MethodContextSnapshot Method = "context.snapshot"

type ContextSectionKey string

const (
	ContextChildren ContextSectionKey = "dyna_children"
	ContextRequests ContextSectionKey = "dyna_requests"
	ContextPRs      ContextSectionKey = "dyna_prs"
	ContextProfiles ContextSectionKey = "dyna_profiles"
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
