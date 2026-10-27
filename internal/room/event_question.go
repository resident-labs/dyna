package room

// QuestionEvent is a note about a question. A withdrawn question keeps its
// place on the question's own card instead of arriving as a message from the
// session that withdrew it.
type QuestionEvent struct {
	// QuestionID is the question's short ID, such as q1.
	QuestionID string `json:"questionId"`
	// Post is the question's own post, which the note folds into.
	Post string `json:"post"`
	// By is who withdrew the question: the owner, root, or a session address.
	By     string `json:"by,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Detail is the note the agent read.
	Detail string `json:"detail,omitempty"`
}

func (QuestionEvent) eventFamily() EventFamily { return FamilyQuestion }
func (QuestionEvent) eventType() EventType     { return TypeWithdrawn }
func (q QuestionEvent) eventSubject() string   { return q.Post }
func (q QuestionEvent) eventDedupe() string    { return q.QuestionID }
