package dynamcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/contract"
)

// agyLimit is the stored size agy declares a tool up to: it kept github's 1802 B and dropped dyna's 1861 B. The listing stays under 1500.
const agyLimit = 1500

// discordLimit is the same for the discord tool, which only a box with Discord lists and which is the longest at 1543 B.
const discordLimit = 1700

func rpc(t *testing.T, h http.Handler, body string) json.RawMessage {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(w.Body.String())
	}
	return out.Result
}

func TestEveryAgyToolIsUnderTheDeclaredLimit(t *testing.T) {
	server, err := New(nil, nil, post.Preparer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rpc(t, server.Agy(), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != len(server.list) {
		t.Fatalf("%d tools listed, want %d", len(listed.Tools), len(server.list))
	}
	total := 0
	for _, tool := range listed.Tools {
		// The shape agy stores: name, description and parameters.
		stored, _ := json.Marshal(struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}{tool.Name, tool.Description, tool.InputSchema})
		total += len(stored)
		t.Logf("%-14s %4d B", tool.Name, len(stored))
		limit := agyLimit
		if tool.Name == "discord" {
			limit = discordLimit
		}
		if len(stored) > limit {
			t.Errorf("%s is %d B stored, over %d", tool.Name, len(stored), limit)
		}
		if strings.Contains(string(tool.InputSchema), `"description"`) {
			t.Errorf("%s keeps a property description", tool.Name)
		}
	}
	t.Logf("total %d B", total)
}

func TestTheFullListingIsUnchangedByTheAgyOne(t *testing.T) {
	server, err := New(nil, nil, post.Preparer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Tools []Tool `json:"tools"`
	}
	want.Tools = server.list
	expected, _ := json.Marshal(want)
	if got := rpc(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); string(got) != string(expected) {
		t.Fatal("the full listing differs from the registry's tools")
	}
}

func TestAgyInitializeCarriesTheHelpAndFullDoesNot(t *testing.T) {
	server, err := New(nil, nil, post.Preparer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`
	var agy, full struct{ Instructions string }
	_ = json.Unmarshal(rpc(t, server.Agy(), init), &agy)
	_ = json.Unmarshal(rpc(t, server, init), &full)
	if full.Instructions != server.Description() {
		t.Fatalf("full instructions: %q", full.Instructions)
	}
	for _, tool := range server.list {
		if !strings.Contains(agy.Instructions, tool.Description) {
			t.Errorf("the instructions lack %s's help", tool.Name)
		}
	}
}

func TestAgyRefusalNamesTheActionsHelp(t *testing.T) {
	server, err := New(nil, nil, post.Preparer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	call := func(h http.Handler, args string) string {
		return string(rpc(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"goal","arguments":`+args+`}}`))
	}
	for _, args := range []string{`{"action":"nope"}`, `{"action":"item","bogus":1}`, `{}`} {
		agy, full := call(server.Agy(), args), call(server, args)
		if !strings.Contains(agy, `isError`) || !strings.Contains(full, `isError`) || len(agy) <= len(full) {
			t.Errorf("%s:\nagy  %s\nfull %s", args, agy, full)
		}
	}
	if got := call(server.Agy(), `{"action":"item","bogus":1}`); !strings.Contains(got, "item: Add text") || !strings.Contains(got, "parent") {
		t.Fatalf("no help for the action: %s", got)
	}
	if got := call(server, `{"action":"item","bogus":1}`); strings.Contains(got, "Add text") {
		t.Fatalf("the full endpoint added help: %s", got)
	}
}

func forwardEcho(context.Context, contract.Method, []byte) ([]byte, *contract.Error) {
	return []byte(`{"echo":true}`), nil
}

// A call that succeeds is the same on both endpoints.
func TestAgyCallsMatchTheFullEndpoint(t *testing.T) {
	server, err := New(forwardEcho, nil, post.Preparer{})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"context","arguments":{"action":"show"}}}`
	if a, b := rpc(t, server.Agy(), body), rpc(t, server, body); string(a) != string(b) || !strings.Contains(string(a), "echo") {
		t.Fatalf("agy %s\nfull %s", a, b)
	}
}

// The field help a call needs before it acts is in the instructions and in the help action, from the registry that the full
// listing's descriptions come from.
func TestAgyHelpCarriesTheFieldHelp(t *testing.T) {
	server, err := New(nil, nil, post.Preparer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	var agy struct{ Instructions string }
	_ = json.Unmarshal(rpc(t, server.Agy(), init), &agy)
	fields := 0
	for _, tool := range server.list {
		var out struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		}
		call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool.Name + `","arguments":{"action":"help"}}}`
		if err := json.Unmarshal(rpc(t, server.Agy(), call), &out); err != nil || out.IsError || len(out.Content) != 1 {
			t.Fatalf("%s help: %+v, %v", tool.Name, out, err)
		}
		if out.Content[0].Text != helpText(tool) || !strings.Contains(agy.Instructions, helpText(tool)) {
			t.Errorf("%s: help differs from the registry's text", tool.Name)
		}
		for key, property := range tool.InputSchema.Properties {
			if property.Description == "" {
				continue
			}
			fields++
			if !strings.Contains(out.Content[0].Text, key+": "+property.Description) {
				t.Errorf("%s.%s: its description is not in the help", tool.Name, key)
			}
		}
	}
	if fields == 0 {
		t.Fatal("no field descriptions were checked")
	}
	// A description that no action's prose says: subscription's latest is stated only in its field.
	subscription, _ := server.Tool("subscription")
	latest := subscription.InputSchema.Properties["latest"].Description
	prose := ""
	for _, a := range server.actions["subscription"] {
		prose += a.help
	}
	if latest == "" || strings.Contains(prose, latest) || !strings.Contains(agy.Instructions, latest) {
		t.Fatalf("latest: %q", latest)
	}
}

// Help is an action of the agy listing only: the full endpoint refuses it as before, and every compact tool lists it.
func TestHelpIsAgyOnly(t *testing.T) {
	server, err := New(nil, nil, post.Preparer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range server.list {
		if slices.Contains(tool.InputSchema.Properties["action"].Enum, agyHelp) {
			t.Errorf("%s: the full listing has a help action", tool.Name)
		}
		compact := compactTool(tool)
		if !slices.Contains(compact.InputSchema.Properties["action"].Enum, agyHelp) || !strings.Contains(compact.Description, "Call action help") {
			t.Errorf("%s: the compact tool does not offer help: %q", tool.Name, compact.Description)
		}
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"goal","arguments":{"action":"help"}}}`
	if got := string(rpc(t, server, call)); !strings.Contains(got, "Unknown action") {
		t.Fatalf("the full endpoint answered help: %s", got)
	}
}
