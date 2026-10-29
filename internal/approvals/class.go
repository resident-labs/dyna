// internal/approvals/class.go
package approvals

// Class says who may approve a held agent write.
type Class string

const (
	// ClassRead runs at once and is never held.
	ClassRead Class = "read"
	// ClassAgent may be approved by any agent ancestor of the requester.
	ClassAgent Class = "agent"
	// ClassOwner may be approved only by the owner.
	ClassOwner Class = "owner"
)
