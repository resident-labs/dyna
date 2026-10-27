package notify

import (
	"strings"
	"testing"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// chain is a root, one session under it and a leaf under that session.
type chain struct {
	e                 *testEnv
	rec               room.Record
	root, mid, leaf   Sender
	rootInfo, midInfo subagent.Info
	leafInfo          subagent.Info
}

func newChain(t *testing.T) chain {
	e := newTestEnv(t)
	rec := e.room()
	e.human(rec, "SLEEP:0 RESPOND: root ready", "@root")
	root := e.rootSession(rec)
	mid := e.start(rec, "mid", nil)
	leaf := e.start(rec, "leaf", &mid)
	sender := func(info subagent.Info) Sender { return Sender{Kind: SenderSession, Room: rec.Name, Session: info} }
	return chain{e: e, rec: rec, root: sender(root), mid: sender(mid), leaf: sender(leaf), rootInfo: root, midInfo: mid, leafInfo: leaf}
}

func (c chain) file(t *testing.T, reason string) questions.Question {
	t.Helper()
	q, err := c.e.svc.FileTerminalRequest(c.e.ctx, c.leaf, TerminalRequestInput{Argv: []string{"gh", "auth", "login"}, Cwd: "/work", Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// terminalPost finds the typed done post that is addressed to a session.
func (c chain) terminalPost(t *testing.T, to subagent.Info) (room.Message, bool) {
	t.Helper()
	msgs, err := c.e.svc.Rooms.Messages(c.rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Kind == string(room.FamilyTerminal) && m.Notice.Kind == string(room.TypeRequestDone) && m.Addressed(to.Address()) {
			return m, true
		}
	}
	return room.Message{}, false
}

func TestTerminalRequestApprovalClimbsToTheOwner(t *testing.T) {
	c := newChain(t)
	q := c.file(t, "sign in to GitHub")
	if q.Terminal.Stage != questions.StageWaitingSession || q.Terminal.Holder != c.midInfo.ID {
		t.Fatalf("a leaf's request waits for its parent: %+v", q.Terminal)
	}
	for _, other := range []Sender{c.leaf, c.root} {
		if err := c.e.svc.ApproveTerminalRequest(c.e.ctx, other, "", q.ID); err == nil || !strings.Contains(err.Error(), "do not hold") {
			t.Fatalf("a session that does not hold the request approved it: %v", err)
		}
	}
	if err := c.e.svc.ApproveTerminalRequest(c.e.ctx, c.mid, "", q.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.e.svc.OwnerTerminalRequest(c.rec.ID, q.ID); err == nil {
		t.Fatal("the owner saw a request that root had not approved")
	}
	if err := c.e.svc.ApproveTerminalRequest(c.e.ctx, c.root, "", q.ID); err != nil {
		t.Fatal(err)
	}
	if _, got, err := c.e.svc.OwnerTerminalRequest(c.rec.ID, q.ID); err != nil || got.Terminal.Holder != "" {
		t.Fatalf("root's approval did not reach the owner: %+v, %v", got.Terminal, err)
	}
}

func TestTerminalRequestDenialWakesTheFilerWithTheReason(t *testing.T) {
	c := newChain(t)
	q := c.file(t, "sign in to GitHub")
	if err := c.e.svc.DenyTerminalRequest(c.e.ctx, c.mid, "", q.ID, "use the token in the vault"); err != nil {
		t.Fatal(err)
	}
	post, ok := c.terminalPost(t, c.leafInfo)
	if !ok || !strings.Contains(post.Text, "denied") || !strings.Contains(post.Text, "use the token in the vault") {
		t.Fatalf("the filer did not get the denial and its reason: %+v", post)
	}
}

func TestTerminalReportSharesLinesOnlyWhenChosen(t *testing.T) {
	c := newChain(t)
	q := c.file(t, "sign in to GitHub")
	for _, approver := range []Sender{c.mid, c.root} {
		if err := c.e.svc.ApproveTerminalRequest(c.e.ctx, approver, "", q.ID); err != nil {
			t.Fatal(err)
		}
	}
	exit := 0
	if err := c.e.svc.ReportTerminalRequest(c.e.ctx, c.rec.ID, q.ID, questions.TerminalReport{Verdict: questions.VerdictDone, ExitCode: &exit, Note: "logged in"}); err != nil {
		t.Fatal(err)
	}
	post, ok := c.terminalPost(t, c.leafInfo)
	if !ok || !strings.Contains(post.Text, "Exit code 0") || !strings.Contains(post.Text, "logged in") || strings.Contains(post.Text, "token ghp_") {
		t.Fatalf("the result must state exit code and note: %+v", post)
	}
	done, _ := post.TerminalDone()
	if len(done.SharedLines) != 0 {
		t.Fatalf("lines reached the filer without the owner's choice: %v", done.SharedLines)
	}
	if got := terminalDone(questions.Question{ID: "q9", Answer: &questions.Answer{}, Terminal: &questions.TerminalRequest{Stage: questions.StageDone, SharedLines: "line one\nline two"}}, ""); len(got.SharedLines) != 2 {
		t.Fatalf("shared lines were not delivered: %v", got.SharedLines)
	}
}

func TestTerminalExitReportsDoneOrFailedWithItsCode(t *testing.T) {
	c := newChain(t)
	for _, tc := range []struct {
		code    int
		verdict questions.TerminalStage
	}{{0, questions.StageDone}, {3, questions.StageFailed}} {
		q := c.file(t, "run it")
		for _, approver := range []Sender{c.mid, c.root} {
			if err := c.e.svc.ApproveTerminalRequest(c.e.ctx, approver, "", q.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.e.svc.ReportTerminalExit(c.e.ctx, c.rec.ID, q.ID, tc.code); err != nil {
			t.Fatal(err)
		}
		_, got, _ := c.e.svc.OwnedTerminalRequest(c.rec.ID, q.ID)
		if got.Terminal.Stage != tc.verdict || got.Terminal.ExitCode == nil || *got.Terminal.ExitCode != tc.code {
			t.Fatalf("exit %d ended as %+v", tc.code, got.Terminal)
		}
	}
}

func TestOwnerReadsAReportedRequestOnlyAsDoneOrFailed(t *testing.T) {
	c := newChain(t)
	q := c.file(t, "sign in to GitHub")
	for _, approver := range []Sender{c.mid, c.root} {
		if err := c.e.svc.ApproveTerminalRequest(c.e.ctx, approver, "", q.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.e.svc.ReportTerminalRequest(c.e.ctx, c.rec.ID, q.ID, questions.TerminalReport{Verdict: questions.VerdictFailed}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.e.svc.OwnedTerminalRequest(c.rec.ID, q.ID); err != nil {
		t.Fatalf("the owner cannot read the failed request: %v", err)
	}
	other := c.file(t, "second")
	if _, _, err := c.e.svc.OwnedTerminalRequest(c.rec.ID, other.ID); err == nil {
		t.Fatal("the owner read a request that is not theirs")
	}
}

func TestSecondTerminalRequestReplacesTheFirst(t *testing.T) {
	c := newChain(t)
	first := c.file(t, "first")
	second := c.file(t, "second")
	list, err := c.e.svc.QuestionStore().List(c.rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range list {
		switch {
		case q.ID == first.ID && (q.State != "withdrawn" || q.Withdrawn.Reason != "replaced"):
			t.Fatalf("the older request stayed open: %+v", q)
		case q.ID == second.ID && q.State != "open":
			t.Fatalf("the new request is %s", q.State)
		}
	}
}

func TestClosingAHolderTellsTheNextHolder(t *testing.T) {
	c := newChain(t)
	q := c.file(t, "sign in to GitHub")
	// This is the move a closing holder makes, and the hook the manager calls.
	moved, err := c.e.svc.QuestionStore().ReassignHolder(c.rec.ID, c.midInfo.ID, c.rootInfo.ID)
	if err != nil || len(moved) != 1 {
		t.Fatalf("moved %d requests: %v", len(moved), err)
	}
	if err := c.e.svc.RepostTerminalRequests(c.e.ctx, moved); err != nil {
		t.Fatal(err)
	}
	list, _ := c.e.svc.QuestionStore().List(c.rec.ID)
	for _, got := range list {
		if got.ID == q.ID && got.Terminal.Holder != c.rootInfo.ID {
			t.Fatalf("the request did not move to root: %+v", got.Terminal)
		}
	}
	msgs, _ := c.e.svc.Rooms.Messages(c.rec.ID)
	for _, m := range msgs {
		if ev, ok := m.TerminalRequest(); ok && ev.Holder == c.rootInfo.Address() {
			return
		}
	}
	t.Fatal("the new holder got no request post")
}

func TestEveryEndedTerminalStageMapsToAVerdict(t *testing.T) {
	for _, stage := range questions.TerminalStages {
		verdict, ok := terminalVerdict(stage)
		if ok != stage.Ended() || (ok && verdict == "") {
			t.Errorf("stage %q: ended=%v but maps to (%q, %v)", stage, stage.Ended(), verdict, ok)
		}
	}
	for _, v := range []questions.TerminalVerdict{questions.VerdictDone, questions.VerdictFailed, questions.VerdictDeclined} {
		stage := v.Stage()
		if got, ok := terminalVerdict(stage); !ok || string(got) != string(v) {
			t.Errorf("verdict %q ends at %q, which maps back to %q", v, stage, got)
		}
	}
}
