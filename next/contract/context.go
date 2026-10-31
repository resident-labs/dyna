package contract

const MethodContextSnapshot Method = "context.snapshot"

type AgentSurface string

const (
	AgentSurfaceOff      AgentSurface = "off"
	AgentSurfaceCaller   AgentSurface = "caller"
	AgentSurfaceDeclared AgentSurface = "declared"
)

// ContextSnapshotRequest selects one section. An empty section requests all sections.
type ContextSnapshotRequest struct {
	Section string `json:"section,omitempty"`
}
type ContextSnapshotResponse struct {
	Sections []ContextSection `json:"sections"`
}
type ContextSection struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}
