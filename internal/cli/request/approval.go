package request

import (
	"strings"

	"dyna-agent/internal/approvals"
	"github.com/spf13/cobra"
)

func approvalID(id string) bool { return strings.HasPrefix(id, "A-") }

func decideApproval(c *cobra.Command, roomRef, id string, action approvals.Action, reason string) error {
	return call(c, roomRef, "ApprovalDecide", func(ref string) []any {
		return []any{approvals.DecisionInput{Room: ref, RequestID: id, Action: action, Reason: reason}}
	})
}
