// Package archive verifies reports from closing boxes.
package archive

import (
	"context"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

type Remote interface {
	Ref(context.Context, contract.RepoName, string) (string, error)
}

type Verifier struct{ Remote Remote }

func (v *Verifier) Verify(ctx context.Context, session contract.SessionID, report contract.ArchiveDoneRequest) error {
	ref := "refs/dyna/archive/" + string(session)
	if report.Skipped {
		ref = "refs/heads/v2/" + string(session)
	}
	hash, err := v.Remote.Ref(ctx, report.Repo, ref)
	if err != nil {
		return err
	}
	if hash != report.Commit {
		return errors.New("archive ref mismatch")
	}
	return nil
}
