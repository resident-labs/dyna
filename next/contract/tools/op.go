package tools

import "github.com/resident-labs/dyna/contract"

func opEntries() []Entry {
	return []Entry{
		entry("op", "decide", contract.MethodOpDecide, typ[contract.OpDecideRequest](), Owner, "Decide an operation", "op approve", "op deny"),
		entry("op", "withdraw", contract.MethodOpWithdraw, typ[contract.OpWithdrawRequest](), Session, "Withdraw an operation", "op withdraw"),
	}
}
