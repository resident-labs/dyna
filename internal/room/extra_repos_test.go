package room

import (
	"os"
	"testing"
)

func TestExtraRepoRegistersAndShowsAMissingOneInsteadOfFailing(t *testing.T) {
	s := recordsStore(t)
	main, sidecar := newRepo(t, "main"), newRepo(t, "sidecar")
	if _, err := s.Create(CreateInput{Name: "alpha", Repo: main}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRepo("alpha", sidecar); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRepo("alpha", sidecar); err == nil {
		t.Fatal("a second registration of the same repository succeeded")
	}
	if err := s.AddRepo("alpha", main); err == nil {
		t.Fatal("the room's own repository registered as an extra one")
	}
	rec, err := s.Resolve("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Repos(); len(got) != 2 || !got[0].Primary || got[1].Path != sidecar || got[1].Missing {
		t.Fatalf("repos = %+v", got)
	}
	if err := os.RemoveAll(sidecar); err != nil {
		t.Fatal(err)
	}
	if rec, err = s.Resolve("alpha"); err != nil || !rec.Repos()[1].Missing {
		t.Fatalf("a repository that left the disk is not listed as missing: %+v, %v", rec.Repos(), err)
	}
	if err := s.RemoveRepo("alpha", sidecar); err != nil {
		t.Fatalf("remove a missing repository: %v", err)
	}
	if rec, _ = s.Resolve("alpha"); len(rec.ExtraRepos) != 0 {
		t.Fatalf("extra repos = %v", rec.ExtraRepos)
	}
}
