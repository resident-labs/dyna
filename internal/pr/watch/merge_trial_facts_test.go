package watch

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/store/storetest"
)

// A GraphQL batch answers several repositories at once, and GitHub reports a
// per-field failure as a top-level error. One repository without
// .github/workflows must not hide another repository's trial pipeline, and a
// read that proves nothing must keep the last conclusive answer.
func TestMergeTrialFactSurvivesAPartialBatchAndAFailedRead(t *testing.T) {
	storetest.DataDir(t)
	observed := time.Date(2099, 1, 3, 12, 0, 0, 0, time.UTC)
	now := observed
	s := &Service{Store: &Store{Root: filepath.Join(t.TempDir(), "prs")}, Now: func() time.Time { return now }}
	body := ""
	s.GH = GitHub{now: s.Now, run: func(_ context.Context, cmd *exec.Cmd) error {
		if _, err := fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+body); err != nil {
			return err
		}
		if body == partialBatch {
			// gh exits non-zero for a GraphQL errors array while the data is
			// still usable, which is how the watcher receives a partial batch.
			return fmt.Errorf("gh: Could not resolve file for path '.github/workflows'.")
		}
		return nil
	}}
	if err := s.Store.update(func(st *State) error {
		for _, repo := range []string{"owner/one", "owner/two"} {
			key := "github.com/" + repo + "#1"
			st.Records[key] = &Record{Key: key, Host: "github.com", Repo: repo, Number: 1, RoomID: "room-1",
				Watching: true, State: "open", Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Created: observed.Add(-48 * time.Hour), Checked: observed.Add(-time.Hour)}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	refresh := func() State {
		t.Helper()
		st, err := s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.refreshHeads(t.Context(), st, now, nil); err != nil {
			t.Fatal(err)
		}
		read, err := s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		return read
	}

	body = partialBatch
	st := refresh()
	if got := st.RepositoryFacts["github.com/owner/two"]; !got.MergeTrialCI {
		t.Fatalf("a partial batch dropped the trial pipeline of the repository that has one: %+v", got)
	}
	if got := st.RepositoryFacts["github.com/owner/one"]; got.MergeTrialCI {
		t.Fatalf("a repository without workflows claimed a trial pipeline: %+v", got)
	}

	// The next read returns no workflow files for owner/two at all. That proves
	// nothing, so the last conclusive answer stands.
	body = unreadableBatch
	st = refresh()
	if got := st.RepositoryFacts["github.com/owner/two"]; !got.MergeTrialCI {
		t.Fatalf("an unreadable workflow read dropped the last good trial fact: %+v", got)
	}
}

const trialWorkflows = `{"object":{"entries":[{"name":"ci.yml","type":"blob","object":{"text":"on:\n  push:\n    branches: [main, 'dyna/merge-trial/**']\n","isTruncated":false}}]}}`

func trialNode() string {
	return `{"number":1,"title":"Change","state":"OPEN","merged":false,"isDraft":false,"mergeable":"MERGEABLE","updatedAt":"2099-01-03T12:00:00Z","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","headRefName":"feature","headRepository":{"nameWithOwner":"owner/repo"},"baseRefOid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","baseRefName":"main","author":{"login":"owner","__typename":"User"},"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS","contexts":{"nodes":[]}}}}]}}`
}

var partialBatch = `{"errors":[{"type":"NOT_FOUND","path":["r0","defaultBranchRef","target","workflows"],"message":"Could not resolve file for path '.github/workflows'."}],"data":{` +
	`"r0":{"defaultBranchRef":{"name":"main","target":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","statusCheckRollup":{"state":"SUCCESS","contexts":{"totalCount":0}},"workflows":null}},"p1":` + trialNode() + `},` +
	`"r1":{"defaultBranchRef":{"name":"main","target":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","statusCheckRollup":{"state":"SUCCESS","contexts":{"totalCount":0}},"workflows":` + trialWorkflows + `}},"p1":` + trialNode() + `}}}`

var unreadableBatch = `{"data":{` +
	`"r0":{"defaultBranchRef":{"name":"main","target":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","statusCheckRollup":{"state":"SUCCESS","contexts":{"totalCount":0}},"workflows":null}},"p1":` + trialNode() + `},` +
	`"r1":{"defaultBranchRef":{"name":"main","target":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","statusCheckRollup":{"state":"SUCCESS","contexts":{"totalCount":0}}}},"p1":` + trialNode() + `}}}`
