package contract

// Interrupt is an engine-checked sender above the receiving session. It is carried
// only on steering input; reply destinations do not establish sender identity.
type Interrupt struct {
	Kind InterruptKind `json:"kind"`
	Name string        `json:"name,omitempty"`
}
type InterruptKind string

const (
	InterruptOwner    InterruptKind = "owner"
	InterruptParent   InterruptKind = "parent"
	InterruptAncestor InterruptKind = "ancestor"
)

func (i Interrupt) Sender() string {
	switch i.Kind {
	case InterruptOwner:
		return "The owner"
	case InterruptParent:
		return "Your parent " + i.Name
	default:
		return i.Name + ", above you,"
	}
}
