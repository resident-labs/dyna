package engine

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	enginegit "github.com/resident-labs/dyna/engine/git"
)

// endRun ends a crew's one turn, which ends its run.
func (w workflowCase) endRun(crew contract.Principal) {
	w.t.Helper()
	start := w.turn(crew)
	if _, problem := w.TurnOutput(w.ctx, crew, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Model: "m"}); problem != nil {
		w.t.Fatal(problem)
	}
}

func (w workflowCase) resume(run contract.WorkflowRunID) (contract.WorkflowRunResponse, contract.Principal) {
	w.t.Helper()
	next, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Resume: run})
	if problem != nil {
		w.t.Fatal(problem)
	}
	return next, contract.Principal{Kind: contract.PrincipalSession, Session: next.Session}
}

func (w workflowCase) canPush(repo contract.RepoName, session contract.SessionID, branch string) bool {
	w.t.Helper()
	allowed, err := enginegit.CanPushBranch(w.ctx, w.store, repo, session, branch)
	if err != nil {
		w.t.Fatal(err)
	}
	return allowed
}

func TestAResumedRunOwnsTheBranchesOfItsChainAndNoOthers(t *testing.T) {
	const repo contract.RepoName = "owner/repo"
	w := setupWorkflow(t)
	if err := w.store.AddRoomRepo(w.ctx, w.room, repo, true); err != nil {
		t.Fatal(err)
	}
	first, firstCrew := w.start(w.owner, workflowScript)
	w.endRun(firstCrew)
	second, secondCrew := w.resume(first.Run)
	w.endRun(secondCrew)
	third, _ := w.resume(second.Run)
	branch := func(session contract.SessionID, topic string) string { return "v2/" + string(session) + topic }

	for _, want := range []string{branch(first.Session, ""), branch(first.Session, "-0123456789ab"), branch(first.Session, "-run"), branch(second.Session, "-0123456789ab")} {
		if !w.canPush(repo, third.Session, want) {
			t.Errorf("the third run cannot push %s", want)
		}
	}
	if !w.canPush(repo, second.Session, branch(first.Session, "-0123456789ab")) {
		t.Error("the second run cannot push the first run's branch")
	}

	// A resume reaches back along its own chain and nowhere else.
	other, _ := w.resume(first.Run)
	unrelated, _ := w.start(w.owner, workflowScript)
	refused := []struct {
		name    string
		repo    contract.RepoName
		session contract.SessionID
		branch  string
	}{
		{"an earlier run onto a later one", repo, first.Session, branch(third.Session, "-0123456789ab")},
		{"a run onto its sibling", repo, other.Session, branch(second.Session, "-0123456789ab")},
		{"a run that resumes nothing", repo, unrelated.Session, branch(first.Session, "-0123456789ab")},
		{"a session that is not a crew", repo, w.root, branch(first.Session, "-0123456789ab")},
		{"another repository", "owner/other", third.Session, branch(first.Session, "-0123456789ab")},
		{"the default branch", repo, third.Session, "main"},
		{"a path under the branch", repo, third.Session, branch(first.Session, "/topic")},
		{"a bad topic", repo, third.Session, branch(first.Session, "-a..b")},
	}
	for _, c := range refused {
		if w.canPush(c.repo, c.session, c.branch) {
			t.Errorf("%s: %s pushed %s", c.name, c.session, c.branch)
		}
	}

	// The chain holds only inside one room and repository, whatever the stored rows say.
	theirs, problem := w.RoomCreate(w.ctx, w.owner, contract.RoomCreateRequest{Name: "theirs", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	move := func(column string, value any) {
		t.Helper()
		err := w.store.Write(w.ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(w.ctx, "UPDATE workflow_runs SET "+column+"=? WHERE id=?", value, second.Run)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	move("room", theirs.Room)
	if w.canPush(repo, third.Session, branch(second.Session, "-0123456789ab")) || w.canPush(repo, third.Session, branch(first.Session, "-0123456789ab")) {
		t.Error("the chain crossed into another room")
	}
	move("room", w.room)
	move("repo", "owner/other")
	if w.canPush(repo, third.Session, branch(second.Session, "-0123456789ab")) || w.canPush(repo, third.Session, branch(first.Session, "-0123456789ab")) {
		t.Error("the chain crossed into another repository")
	}
}

// The live failure: a script passed root's session ID as post's mention. Length plays no part, and neither does the run's stop.
// A session's ID names it as its name does.
func TestAPostMentionsAName(t *testing.T) {
	w := setupWorkflow(t)
	_, crew := w.start(w.owner, workflowScript)
	text := strings.Repeat("x", 4892)
	if _, problem := w.Post(w.ctx, crew, contract.PostRequest{Room: w.room, Text: text, Mentions: []string{string(w.root)}}); problem != nil {
		t.Fatalf("a session ID as a mention: %+v", problem)
	}
	if _, problem := w.Post(w.ctx, crew, contract.PostRequest{Room: w.room, Text: text, Mentions: []string{"root"}}); problem != nil {
		t.Fatalf("root as a mention: %+v", problem)
	}
}

func TestClosingACrewThatClosedItselfSaysSo(t *testing.T) {
	w := setupWorkflow(t)
	started, crew := w.start(w.owner, workflowScript)
	w.endRun(crew)
	err := w.store.Write(w.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(w.ctx, "UPDATE sessions SET closed_at=1 WHERE id=?", started.Session)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, problem := w.SessionClose(w.ctx, w.owner, contract.SessionCloseRequest{Session: started.Session})
	if problem == nil || problem.Code != contract.ErrNotFound || !strings.Contains(problem.Message, "session "+string(started.Session)+" is closed") || strings.Contains(problem.Message, "sql:") {
		t.Fatalf("%+v", problem)
	}
}
