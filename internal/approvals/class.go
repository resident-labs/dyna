// internal/approvals/class.go
package approvals

// Class says who may approve a held agent write.
type Class string

const (
	// ClassRead runs at once and is never held.
	ClassRead Class = "read"
	// ClassAuto is an engine-verified routine write that runs without a decision.
	ClassAuto Class = "auto"
	// ClassAgent may be approved by any agent ancestor of the requester.
	ClassAgent Class = "agent"
	// ClassOwner may be approved only by the owner.
	ClassOwner Class = "owner"
)
