package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/library"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

// Refusing the transport must leave the map unchanged for every mutation.
func TestLibraryMutationsReturnEngineFailure(t *testing.T) {
	data := storetest.Isolate(t)
	repo := authorityRepo(t)
	calls := map[string]websvc.Call{}
	for _, name := range []string{"AddLibraryEntry", "UpdateLibraryEntry", "LinkLibraryEntry", "RetireLibraryEntry"} {
		calls[name] = func(context.Context, []json.RawMessage) (any, error) {
			return nil, errors.New("engine refused library change")
		}
	}
	enginetest.Serve(t, data, calls)
	for _, args := range [][]string{
		{"add", "entry", "--name", "Entry", "--summary", "Entry.", "--owns", "agents/**"},
		{"update", "entry", "--summary", "Changed."},
		{"link", "entry", "--docs", "README.md"},
		{"link", "entry", "--remove-docs", "README.md"},
		{"retire", "entry"},
	} {
		c := Command(Options{DataDir: data})
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(append(args, "--repo", repo))
		if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "engine refused library change") {
			t.Errorf("%v: %v", args, err)
		}
	}
	rows, err := (library.Store{DataDir: data}).List("example.com/library", true)
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed engine call changed map: %v, %v", rows, err)
	}
}

// serveLibrary supplies the store boundary for existing command output tests.
// The fixture actor is explicit; request data cannot choose it.
func serveLibrary(t *testing.T, data string, actor authority.Actor) {
	t.Helper()
	st := library.Store{DataDir: data, Actor: actor}
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "AddLibraryEntry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: st.Add},
		{Name: "UpdateLibraryEntry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(repo, id string, p library.Patch) (library.Entry, error) { return st.Update(repo, id, p.Apply) }},
		{Name: "LinkLibraryEntry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(repo, id string, add, remove library.Links) (library.Entry, error) {
			return st.Update(repo, id, func(e *library.Entry) error {
				e.Docs, e.Tests = library.Relink(e.Docs, add.Docs, remove.Docs), library.Relink(e.Tests, add.Tests, remove.Tests)
				return nil
			})
		}},
		{Name: "RetireLibraryEntry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: st.Retire},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, data, calls)
}
