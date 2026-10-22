package librarian

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

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
