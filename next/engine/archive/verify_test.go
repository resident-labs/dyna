package archive

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type fakeRemote struct {
	ref  string
	hash string
}

func (r *fakeRemote) Ref(_ context.Context, _ contract.RepoName, ref string) (string, error) {
	r.ref = ref
	return r.hash, nil
}

func TestVerifyChecksArchiveRef(t *testing.T) {
	r := &fakeRemote{hash: "abc"}
	err := (&Verifier{Remote: r}).Verify(context.Background(), "ses_1", contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"})
	if err != nil || r.ref != "refs/dyna/archive/ses_1" {
		t.Fatalf("ref %q, error %v", r.ref, err)
	}
}
func TestVerifyRejectsChangedBranchTip(t *testing.T) {
	r := &fakeRemote{hash: "different"}
	err := (&Verifier{Remote: r}).Verify(context.Background(), "ses_1", contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc", Skipped: true})
	if err == nil || r.ref != "refs/heads/v2/ses_1" {
		t.Fatalf("ref %q, error %v", r.ref, err)
	}
}
