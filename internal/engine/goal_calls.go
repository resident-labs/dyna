package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/websvc"
)

// The engine's caller policies, one per binding that a mutation acts for.
// callerPolicies says what each value does.
const (
	policyGoal        websvc.Policy = iota + 1 // changing goals or rules; records the goal author on ctx
	policySession                              // changing a session
	policyCLI                                  // using CLI commands
	policyPR                                   // changing PR state
	policyMachine                              // changing a node
	policyOwner                                // using owner controls
	policyIssue                                // triaging room issues
	policyOwnerOrRoot                          // owner or root controls that act on a room's work
)

// callerPolicy is what one policy value does. doing names the action in
// caller-resolution errors and logs, with adds anything else the call needs
// on ctx (by names who acted), and check, when set, refuses callers the
// policy does not cover. check runs inside the actor wrapper, so it sees the
// actor that wrapper resolved.
type callerPolicy struct {
	doing string
	with  func(ctx context.Context, by string) context.Context
	check func(ctx context.Context, name string, args []json.RawMessage) error
}

// callerPolicies is the engine's policy table, built once.
var callerPolicies = map[websvc.Policy]callerPolicy{
	policyGoal:        {doing: "changing goals or rules", with: roomsvc.WithGoalAuthor},
	policySession:     {doing: "changing a session"},
	policyCLI:         {doing: "using CLI commands"},
	policyPR:          {doing: "changing PR state"},
	policyMachine:     {doing: "changing a node", check: checkMachineWrite},
	policyOwner:       {doing: "using owner controls", check: ownerOnly},
	policyIssue:       {doing: "triaging room issues"},
	policyOwnerOrRoot: {doing: "using owner or root controls", check: ownerOrRoot},
}

// applyCallerPolicies makes each call that declares a caller policy act for
// its mutation actor: the resolved caller, or, for a report-only unverified
// caller, its claimed session (mutationActor). So a CLI call and a web call
// meet the same authority checks, which read authority.Of(ctx). calls comes
// from websvc.Bind(table), so every policy names a bound call. A policy value
// the engine does not know fails here, before the server starts.
func applyCallerPolicies(calls map[string]websvc.Call, table []websvc.Binding) error {
	for _, binding := range table {
		if binding.Policy == 0 {
			continue
		}
		policy, ok := callerPolicies[binding.Policy]
		if !ok {
			return fmt.Errorf("engine: %s carries unknown caller policy %d", binding.Name, binding.Policy)
		}
		call := calls[binding.Name]
		if policy.check != nil {
			check, name := policy.check, binding.Name
			inner := call
			call = func(ctx context.Context, args []json.RawMessage) (any, error) {
				if err := check(ctx, name, args); err != nil {
					return nil, err
				}
				return inner(ctx, args)
			}
		}
		doing, with := policy.doing, policy.with
		calls[binding.Name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			caller, local := websvc.CallerFromContext(ctx)
			actor, by, err := mutationActor(caller, local, authority.Of(ctx), callspec.ClaimOf(ctx), enforceCallers, log.Printf, doing)
			if err != nil {
				return nil, err
			}
			ctx = authority.WithActor(ctx, actor)
			if with != nil {
				ctx = with(ctx, by)
			}
			return call(ctx, args)
		}
	}
	return nil
}
