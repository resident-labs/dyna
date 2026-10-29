// Approval requests. Source: the ApprovalDecide payload in internal/approvals
// and its engine binding in internal/engine/approvals.go.

// ApprovalOpen is one request that still waits for a decision. at is the
// session that must decide now, or "owner". The engine, not the loaded feed
// page, says which requests are open.
export interface ApprovalOpen {
  id: string
  at: string
}

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
