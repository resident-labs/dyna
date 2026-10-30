package transcript

// StepKind is the kind of work one tool call did. It is a closed set, so the
// session view draws a designed row for each kind instead of showing a tool ID.
type StepKind string

// Step kinds. StepTool covers every tool the view names, such as an MCP tool.
const (
	StepKindRead     StepKind = "read"
	StepKindEdit     StepKind = "edit"
	StepKindWrite    StepKind = "write"
	StepKindSearch   StepKind = "search"
	StepKindList     StepKind = "list"
	StepKindRun      StepKind = "run"
	StepKindFetch    StepKind = "fetch"
	StepKindDelegate StepKind = "delegate"
	StepKindPlan     StepKind = "plan"
	StepKindMonitor  StepKind = "monitor"
	StepKindTool     StepKind = "tool"
)
