package room

// ReviewKind is the typed result of a reviewer turn.
type ReviewKind string

// Review kinds.
const (
	ReviewApprove     ReviewKind = "approve"
	ReviewChanges     ReviewKind = "changes"
	ReviewApproveFast ReviewKind = "approve-fast"
	ReviewStopEarly   ReviewKind = "stop-early"
)

// reviewKinds lists every review kind the web needs as a union type.
var reviewKinds = []ReviewKind{ReviewApprove, ReviewChanges, ReviewApproveFast, ReviewStopEarly}

// ReviewSeverity says how strongly a finding asks for a change.
type ReviewSeverity string

// Review severities.
const (
	ReviewBlocking  ReviewSeverity = "blocking"
	ReviewShouldFix ReviewSeverity = "should-fix"
	ReviewNit       ReviewSeverity = "nit"
)

// ReviewFinding is one typed finding of a changes review. It is the fact the
// card lists, not the Markdown the reviewer wrote.
type ReviewFinding struct {
	ID       string         `json:"id"`
	Severity ReviewSeverity `json:"severity"`
	File     string         `json:"file"`
	Line     int            `json:"line"`
	Why      string         `json:"why"`
	Fix      string         `json:"fix"`
	Patch    string         `json:"patch,omitempty"`
}

// ReviewStopReason is why a reviewer stopped before it judged the change.
type ReviewStopReason string

// Review stop reasons.
const (
	ReviewSuperseded ReviewStopReason = "superseded"
	ReviewDuplicate  ReviewStopReason = "duplicate"
	ReviewEmpty      ReviewStopReason = "empty"
	ReviewTooLarge   ReviewStopReason = "too-large"
)

// ReviewEvent tells a room how one reviewer turn ended: the verdict, the
// findings by severity, or a stop reason. It is folded
// with the events of one PR head, so a re-review of a new head is a new card.
type ReviewEvent struct {
	Kind     ReviewKind `json:"kind"`
	ChangeID string     `json:"changeId"`
	Number   int        `json:"number,omitempty"`
	Label    string     `json:"label"`
	Location string     `json:"location"`
	Head     string     `json:"head"`
	// Summary is the reviewer's one-line facts line, when it has one.
	Summary string `json:"summary,omitempty"`
	// Findings lists a changes review's findings, newest order kept.
	Findings []ReviewFinding `json:"findings,omitempty"`
	// Blocking, ShouldFix and Nits count the findings by severity, so the chip
	// never recounts a list the card may not show.
	Blocking  int `json:"blocking,omitempty"`
	ShouldFix int `json:"shouldFix,omitempty"`
	Nits      int `json:"nits,omitempty"`
	// StopReason is set only by a stop-early result.
	StopReason ReviewStopReason `json:"stopReason,omitempty"`
	// FastLand reports an approve-fast that uses direct publication. Keeping
	// this Boolean also preserves the meaning of older review events.
	FastLand bool `json:"fastLand,omitempty"`
	// Detail is the reviewer's Markdown body, which the agent text appends and
	// no card parses.
	Detail string `json:"detail,omitempty"`
}

func (ReviewEvent) eventFamily() EventFamily { return FamilyReview }
func (ReviewEvent) eventType() EventType     { return TypeResult }

// eventSubject keeps the events of one PR head together and apart from another
// head's result.
func (e ReviewEvent) eventSubject() string { return e.ChangeID + "\x00" + e.Head }

// eventDedupe keys one result per kind and head: a retried verdict keeps its
// post, and a different result on the same head posts again.
func (e ReviewEvent) eventDedupe() string { return string(e.Kind) }

// eventThread joins review results to their change's lifecycle.
func (e ReviewEvent) eventThread() string {
	if e.ChangeID == "" {
		return ""
	}
	return "pr:" + e.ChangeID
}
