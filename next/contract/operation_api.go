package contract

import "time"

const (
	MethodOpSubmit   Method = "op.submit"
	MethodOpDecide   Method = "op.decide"
	MethodOpWithdraw Method = "op.withdraw"
	MethodOpList     Method = "op.list"
)

type OpSubmitRequest struct {
	Operation Operation `json:"operation"`
}
type OpSubmitResponse struct {
	Operation Operation `json:"operation"`
}
type OpDecideRequest struct {
	ID      OpID   `json:"id"`
	Approve bool   `json:"approve"`
	Reason  string `json:"reason,omitempty"`
}
type OpDecideResponse struct{}
type OpWithdrawRequest struct {
	ID OpID `json:"id"`
}
type OpWithdrawResponse struct{}
type OpListRequest struct {
	Room RoomID `json:"room"`
}
type OpListResponse struct {
	Operations []Operation `json:"operations"`
}

type OpRequested struct {
	ID      OpID      `json:"id"`
	Kind    OpKind    `json:"kind"`
	By      Principal `json:"by"`
	Class   Class     `json:"class"`
	Card    Card      `json:"card"`
	Expires time.Time `json:"expires"`
}
type OpDecided struct {
	ID       OpID     `json:"id"`
	State    OpState  `json:"state"`
	Decision Decision `json:"decision"`
}
type OpRan struct {
	ID     OpID     `json:"id"`
	State  OpState  `json:"state"`
	Result OpResult `json:"result"`
}

func (OpRequested) EventType() EventType { return "op.requested" }
func (OpRequested) isEventBody()         {}
func (OpDecided) EventType() EventType   { return "op.decided" }
func (OpDecided) isEventBody()           {}
func (OpRan) EventType() EventType       { return "op.ran" }
func (OpRan) isEventBody()               {}
