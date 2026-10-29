// Approval requests. Source: the ApprovalDecide payload in internal/approvals
// and its engine binding in internal/engine/approvals.go.

// ApprovalDecision is what the owner chooses on a pending request.
export type ApprovalDecision = 'approve' | 'deny' | 'pass'

// ApprovalDecideInput is the one payload of the ApprovalDecide call. The reason
// is required for deny and optional for pass.
export interface ApprovalDecideInput {
  room: string
  requestId: string
  action: ApprovalDecision
  reason: string
}
