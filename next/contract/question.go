package contract

import "time"

const (
	MethodQuestionAsk    Method = "question.ask"
	MethodQuestionAnswer Method = "question.answer"
	MethodQuestionList   Method = "question.list"
)

// QuestionState is where a question stands. A close withdraws what is still pending.
type QuestionState string

const (
	QuestionStatePending   QuestionState = "pending"
	QuestionStateAnswered  QuestionState = "answered"
	QuestionStateWithdrawn QuestionState = "withdrawn"
)

// Question is one question a session asked the owner, under its ask-owner grant.
type Question struct {
	ID       QuestionID    `json:"id"`
	Room     RoomID        `json:"room"`
	Session  SessionID     `json:"session"`
	Text     string        `json:"text"`
	State    QuestionState `json:"state"`
	Answer   string        `json:"answer,omitempty"`
	Asked    time.Time     `json:"asked"`
	Answered time.Time     `json:"answered,omitzero"`
}

type QuestionAskRequest struct {
	Text     string `json:"text"`
	ClientID string `json:"clientId,omitempty"`
}
type QuestionAskResponse struct {
	Question Question `json:"question"`
}
type QuestionAnswerRequest struct {
	ID   QuestionID `json:"id"`
	Text string     `json:"text"`
}
type QuestionAnswerResponse struct{}

// QuestionListRequest names a room. A session may omit it for its own.
type QuestionListRequest struct {
	Room RoomID `json:"room,omitempty"`
}

// QuestionListResponse holds every waiting question and the newest answered ones.
type QuestionListResponse struct {
	Questions []Question `json:"questions"`
}

type QuestionAsked struct {
	ID      QuestionID `json:"id"`
	Session SessionID  `json:"session"`
	Text    string     `json:"text"`
}

// QuestionAnswered carries the question too, so the delivery reads alone.
type QuestionAnswered struct {
	ID       QuestionID `json:"id"`
	Session  SessionID  `json:"session"`
	Question string     `json:"question"`
	Answer   string     `json:"answer"`
}
type QuestionWithdrawn struct {
	ID      QuestionID `json:"id"`
	Session SessionID  `json:"session"`
}

func (QuestionAsked) EventType() EventType     { return "question.asked" }
func (QuestionAsked) isEventBody()             {}
func (QuestionAnswered) EventType() EventType  { return "question.answered" }
func (QuestionAnswered) isEventBody()          {}
func (QuestionWithdrawn) EventType() EventType { return "question.withdrawn" }
func (QuestionWithdrawn) isEventBody()         {}
