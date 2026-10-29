package engine

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/websvc"
)

// policyNames is every call in the engine's table that carries this caller
// policy.
func policyNames(policy websvc.Policy) []string {
	var names []string
	for _, binding := range bindings(services{rooms: &roomsvc.Service{}}) {
		if binding.Policy == policy {
			names = append(names, binding.Name)
		}
	}
	return names
}

// TestCallerPolicyWrapsTheCall proves a declared policy wraps its call: an
// unverified caller is refused with the policy's doing text before the bound
// method runs, while a binding without a policy runs exactly as bound.
func TestCallerPolicyWrapsTheCall(t *testing.T) {
	for policy, config := range callerPolicies {
		t.Run(config.doing, func(t *testing.T) {
			ran := false
			method := func(context.Context) error { ran = true; return nil }
			table := []websvc.Binding{
				{Name: "Guarded", Access: websvc.Write, Scope: websvc.ScopeOperate, Policy: policy, Method: method},
				{Name: "Unguarded", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: method},
			}
			calls, err := websvc.Bind(table)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyCallerPolicies(calls, table); err != nil {
				t.Fatal(err)
			}
			refused := localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner})
			if _, err := calls["Guarded"](refused, nil); err == nil || !strings.Contains(err.Error(), "could not verify who is "+config.doing) {
				t.Fatalf("unverified caller: %v", err)
			}
			if ran {
				t.Fatal("the guarded method ran for an unverified caller")
			}
			if _, err := calls["Unguarded"](refused, nil); err != nil {
				t.Fatalf("a binding without a policy was wrapped: %v", err)
			}
			if !ran {
				t.Fatal("the unguarded method did not run")
			}
		})
	}
}

// TestOwnerPolicyKeepsOwnerOnlyText checks the owner check keeps its exact
// refusal, so CLI output does not change.
func TestOwnerPolicyKeepsOwnerOnlyText(t *testing.T) {
	method := func(context.Context) error { return nil }
	table := []websvc.Binding{{Name: "Guarded", Access: websvc.Write, Scope: websvc.ScopeOperate, Policy: policyOwner, Method: method}}
	calls, err := websvc.Bind(table)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCallerPolicies(calls, table); err != nil {
		t.Fatal(err)
	}
	session := localCallerContext(t, websvc.Caller{Kind: websvc.Session, Session: "ses_1"}, authority.Actor{Role: authority.Session, Session: "ses_1", Room: "room"})
	if _, err := calls["Guarded"](session, nil); err == nil || err.Error() != "Guarded is owner only" {
		t.Fatalf("session caller: %v", err)
	}
}

// TestUnknownCallerPolicyFails checks that a policy value the engine does not
// know fails before the server starts.
func TestUnknownCallerPolicyFails(t *testing.T) {
	table := []websvc.Binding{{Name: "Guarded", Access: websvc.Write, Scope: websvc.ScopeOperate, Policy: websvc.Policy(99), Method: func() error { return nil }}}
	calls, err := websvc.Bind(table)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCallerPolicies(calls, table); err == nil || !strings.Contains(err.Error(), "unknown caller policy") {
		t.Fatalf("unknown policy: %v", err)
	}
}
