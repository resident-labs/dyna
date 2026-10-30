package archive

import (
	"context"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type fakeRunner struct {
	args [][]string
	hash string
}

func (r *fakeRunner) Run(_ context.Context, _ []string, args ...string) ([]byte, error) {
	r.args = append(r.args, args)
	if len(args) > 2 && args[2] == "rev-parse" {
		return []byte(r.hash + "\n"), nil
	}
	return nil, nil
}
func TestVerifyFetchesArchiveRef(t *testing.T) {
	r := &fakeRunner{hash: "abc"}
	v := &Verifier{Data: t.TempDir(), Token: "secret", Runner: r}
	err := v.Verify(context.Background(), "ses_1", contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc"})
	if err != nil || len(r.args) != 3 || r.args[1][len(r.args[1])-1] != "refs/dyna/archive/ses_1" {
		t.Fatalf("calls %v, error %v", r.args, err)
	}
	for _, args := range r.args {
		if strings.Contains(strings.Join(args, " "), "secret") {
			t.Fatal("token in command arguments")
		}
	}
}
func TestVerifyRejectsChangedBranchTip(t *testing.T) {
	r := &fakeRunner{hash: "different"}
	v := &Verifier{Data: t.TempDir(), Token: "secret", Runner: r}
	err := v.Verify(context.Background(), "ses_1", contract.ArchiveDoneRequest{Repo: "owner/repo", Commit: "abc", Skipped: true})
	if err == nil || r.args[1][len(r.args[1])-1] != "refs/heads/v2/ses_1" {
		t.Fatalf("calls %v, error %v", r.args, err)
	}
}
