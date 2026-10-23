package engine

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/library"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestLibraryEngineUsesCallerAuthority(t *testing.T) {
	data := storetest.Isolate(t)
	s := services{}
	entry := library.Entry{ID: "entry", Name: "Entry", Kind: "feature", Summary: "Entry.", Differs: "Old.", Owns: []string{"internal/**"}}
	// The process is the owner. An explicit session context must still be refused.
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Session, Session: "other"}, authority.Actor{Role: authority.Session, Session: "other"})
	if _, err := s.addLibraryEntry(ctx, "repo", entry); err == nil || !strings.Contains(err.Error(), "library add is ") {
		t.Fatalf("session received owner authority: %v", err)
	}
	if rows, err := (library.Store{DataDir: data}).List("repo", true); err != nil || len(rows) != 0 {
		t.Fatalf("refused change: %v, %v", rows, err)
	}
}

func TestLibraryEngineTransport(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	data := store.DataDir()
	var table []websvc.Binding
	for _, b := range bindings(s) {
		if strings.HasSuffix(b.Name, "LibraryEntry") {
			table = append(table, b)
		}
	}
	calls, err := websvc.Bind(table)
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, data, calls)
	client := engineclient.New("")
	invoke := func(method string, args ...any) library.Entry {
		t.Helper()
		var e library.Entry
		if err := client.Call(context.Background(), method, args, &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	invoke("AddLibraryEntry", "repo", library.Entry{ID: "entry", Name: "Entry", Kind: "feature", Summary: "Entry.", Differs: "Old.", Owns: []string{"internal/**"}})
	summary, empty := "Changed.", ""
	invoke("UpdateLibraryEntry", "repo", "entry", library.Patch{Summary: &summary, Differs: &empty})
	invoke("LinkLibraryEntry", "repo", "entry", library.Links{Docs: []string{"README.md", "README.md"}, Tests: []string{"internal/*_test.go"}}, library.Links{})
	invoke("LinkLibraryEntry", "repo", "entry", library.Links{}, library.Links{Tests: []string{"internal/*_test.go"}})
	e := invoke("RetireLibraryEntry", "repo", "entry", "")
	if !e.Retired || e.Summary != summary || e.Differs != "" || e.Name != "Entry" || len(e.Owns) != 1 || len(e.Docs) != 1 || len(e.Tests) != 0 {
		t.Fatalf("mutation result: %+v", e)
	}
	saved, err := (library.Store{DataDir: data}).Get("repo", "entry")
	if err != nil || !saved.Retired {
		t.Fatalf("saved: %+v, %v", saved, err)
	}
}

// Unverified callers cannot gain authority from a claim or the engine actor.
func TestLibraryUnverifiedClaimCannotEditAsOwner(t *testing.T) {
	data := storetest.Isolate(t)
	ctx := callspec.WithClaim(localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner}), callspec.Claim{Session: "other"})
	_, err := (services{}).addLibraryEntry(ctx, "repo", library.Entry{ID: "entry", Name: "Entry", Kind: "feature", Summary: "Entry.", Owns: []string{"internal/**"}})
	if err == nil || !strings.Contains(err.Error(), "could not verify") {
		t.Fatalf("claimed session received owner authority: %v", err)
	}
	rows, err := (library.Store{DataDir: data}).List("repo", true)
	if err != nil || len(rows) != 0 {
		t.Fatalf("refused change: %v, %v", rows, err)
	}
}
