package librarian

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/librarianstate"
)

func TestSmellSessionCanFileAndAnotherSessionCannot(t *testing.T) {
	h := setup(t)
	h.svc.Issues = &fakeIssues{}
	if _, err := (librarianstate.Store{DataDir: h.svc.DataDir}).Update(h.id, false, func(st *librarianstate.State) error {
		st.Session, st.SmellSession = "map-session", "smell-session"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	smell := Smell{Title: "Split runner responsibilities", At: []string{"agents/run.go"}, Symbols: []string{"Run"}, Cost: "Changes need broad review.", Direction: "Separate the responsibilities."}
	if _, err := h.svc.FileSmell(context.Background(), authority.Actor{Role: authority.Session, Session: "smell-session"}, h.repo, "room:library-smells", smell); err != nil {
		t.Fatalf("saved smell session could not file: %v", err)
	}
	for _, session := range []string{"other-session", "map-session"} {
		_, err := h.svc.FileSmell(context.Background(), authority.Actor{Role: authority.Session, Session: session}, h.repo, "room:librarian", smell)
		if err == nil || !strings.HasPrefix(err.Error(), "library smell is ") {
			t.Fatalf("session %q filed a smell: %v", session, err)
		}
	}
}

func TestSmellUsesExplicitActor(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "remote", "add", "origin", "https://example.com/library.git")
	for _, actor := range []authority.Actor{
		{Role: authority.Session, Session: "other"},
		{Role: authority.Worker},
		{},
	} {
		// The caller's identity must survive an engine running as owner.
		_, err := (Service{DataDir: t.TempDir()}).FileSmell(context.Background(), actor, repo, "room:librarian", Smell{})
		if err == nil || !strings.HasPrefix(err.Error(), "library smell is ") {
			t.Errorf("actor %+v: %v", actor, err)
		}
	}
}
