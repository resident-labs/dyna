package room

// RuleVerb is what happened to a rule.
type RuleVerb string

// Rule verbs.
const (
	RuleAdded   RuleVerb = "added"
	RuleEdited  RuleVerb = "edited"
	RuleRemoved RuleVerb = "removed"
)

// RuleEvent tells the owner feed that the room's rules changed. It replaces
// the free-text notice the rule commands used to post.
type RuleEvent struct {
	RuleID string `json:"ruleId"`
	// Scope is "room" or "global".
	Scope string   `json:"scope"`
	Actor string   `json:"actor"`
	Verb  RuleVerb `json:"verb"`
	// Text is the rule's own text, empty when it was removed.
	Text string `json:"text,omitempty"`
	// Sessions lists the open sessions whose instruction note carries this
	// change. The rule row shows one reach count instead of one row per session.
	Sessions []string `json:"sessions,omitempty"`
}

func (RuleEvent) eventFamily() EventFamily { return FamilyRule }
func (RuleEvent) eventType() EventType     { return TypeChanged }
func (r RuleEvent) eventSubject() string   { return r.RuleID }
func (r RuleEvent) eventDedupe() string    { return string(r.Verb) }
