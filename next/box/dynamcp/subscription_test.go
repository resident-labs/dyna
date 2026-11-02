package dynamcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/contract"
)

// The calls a live root made for an hourly wake: each now goes through or says which field takes what.
func TestSubscriptionAddCallsSayWhatEachFieldTakes(t *testing.T) {
	var forwarded []byte
	server, err := New(func(_ context.Context, m contract.Method, body []byte) ([]byte, *contract.Error) {
		if m != contract.MethodSubscriptionAdd {
			t.Fatalf("forwarded %s", m)
		}
		forwarded = body
		return []byte(`{"id":"sub_a"}`), nil
	}, nil, post.Preparer{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, args, want string }{
		{"every in seconds", `{"action":"add","scope":"session","session":"ses_a","every":3600,"wake":"ses_a"}`,
			`every is a duration string such as "1h", "15m" or "90s", not the number 3600`},
		{"wake as an object", `{"action":"add","scope":"session","session":"ses_a","every":"1h","wake":{"session":"ses_a"}}`,
			`wake is a session name or ID as a string, such as "upstream-sync" or "ses_…", not an object`},
		{"the old milliseconds and principal", `{"action":"add","scope":"session","session":"ses_a","every":3600000,"wake":{"kind":"session","session":"ses_a"}}`,
			`every is a duration string such as "1h", "15m" or "90s", not the number 3600000`},
		{"session as an object", `{"action":"add","scope":"session","session":{"id":"ses_a"},"every":"1h","wake":"ses_a"}`,
			`session is a session name or ID as a string, such as "upstream-sync" or "ses_…", not an object`},
	} {
		forwarded = nil
		out := server.call(context.Background(), callParams{Name: "subscription", Arguments: arguments(t, tc.args)})
		var problem contract.Error
		if err := json.Unmarshal(out.StructuredContent, &problem); err != nil {
			t.Fatal(err)
		}
		if !out.IsError || problem.Code != contract.ErrInvalid || problem.Message != tc.want || forwarded != nil {
			t.Errorf("%s: %+v, forwarded %s", tc.name, problem, forwarded)
		}
	}
	out := server.call(context.Background(), callParams{Name: "subscription", Arguments: arguments(t, `{"action":"add","scope":"session","session":"upstream-sync","every":"1h","wake":"upstream-sync"}`)})
	if out.IsError {
		t.Fatalf("%+v", out)
	}
	var req contract.SubscriptionAddRequest
	if err := json.Unmarshal(forwarded, &req); err != nil {
		t.Fatal(err)
	}
	if req.Scope != contract.ScopeSession || req.Session != "upstream-sync" || req.Every != "1h" || req.Wake != "upstream-sync" {
		t.Fatalf("forwarded %+v", req)
	}
}

func TestSubscriptionFieldsAreDescribed(t *testing.T) {
	list, _, err := registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.Name != "subscription" {
			continue
		}
		for _, field := range []string{"scope", "room", "session", "on", "where", "every", "wake", "run"} {
			if item.InputSchema.Properties[field].Description == "" {
				t.Errorf("%s has no description", field)
			}
		}
		for _, field := range []string{"every", "wake", "session"} {
			if got := item.InputSchema.Properties[field].Type; got != "string" {
				t.Errorf("%s is %s", field, got)
			}
		}
		return
	}
	t.Fatal("no subscription tool")
}
