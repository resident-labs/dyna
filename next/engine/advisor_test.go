package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestBoxHelloGivesRootAndAdvisorTheirSections(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetLayout(box.Layout{Data: "/data", HostData: "/hostdata"})
	if err := s.store.SetEngineHost(ctx, "host", true); err != nil {
		t.Fatal(err)
	}
	plain, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{})
	if problem != nil || plain.Advisor || strings.Contains(plain.Instructions, "# Advisor") {
		t.Fatalf("root before an advisor: %v %+v", problem, plain.Advisor)
	}
	started, problem := s.SessionStart(ctx, ownerCaller, contract.SessionStartRequest{Room: room, Name: "advisor", Profile: "test", Advisor: true})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	hello, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{})
	if problem != nil || !hello.Advisor {
		t.Fatalf("root hello: %v %+v", problem, hello.Advisor)
	}
	want := renderedHead(t, "root-advisor") + fmt.Sprintf("You are root of room room (ID %[1]s), session %[2]s.\nFiles: /box/shared is /data/rooms/%[1]s/shared/ on host.\nStanding rules: the global AGENTS.md is /data/AGENTS.md on host and this room's is /data/rooms/%[1]s/AGENTS.md; bash with host host edits them. The engine copies each room's file to every host for boxes and rewrites that copy at each sync.\nYour box has 1 CPU and 2 GiB of memory: heavy builds belong in a larger child session or CI.", room, root)
	if hello.Instructions != want {
		t.Fatalf("root instructions:\n%s\nwant:\n%s", hello.Instructions, want)
	}
	adviser, problem := s.BoxHello(ctx, boxCaller(started.Session), contract.BoxHelloRequest{})
	if problem != nil || adviser.Advisor || adviser.Root {
		t.Fatalf("advisor hello: %v %+v", problem, adviser)
	}
	want = renderedHead(t, "advisor") + fmt.Sprintf("You are advisor, session %s, in room room (ID %s). Your parent is root.", started.Session, room) + sessionBox
	if adviser.Instructions != want {
		t.Fatalf("advisor instructions:\n%s\nwant:\n%s", adviser.Instructions, want)
	}
}

func TestOneAdvisorPerRoomAndItHoldsNothing(t *testing.T) {
	r := newGoalRig(t)
	start := func(caller contract.Principal, req contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error) {
		req.Room, req.Profile = r.room, "test"
		return r.s.SessionStart(r.ctx, caller, req)
	}
	if _, e := start(asSession(r.lead), contract.SessionStartRequest{Name: "mine", Advisor: true}); e == nil || !strings.Contains(e.Message, "only root or the owner") {
		t.Fatalf("a lead's advisor: %v", e)
	}
	if _, e := start(asSession(r.root), contract.SessionStartRequest{Name: "advisor", Advisor: true, Goal: "work", DoneWhen: "done"}); e == nil || !strings.Contains(e.Message, "holds no goals") {
		t.Fatalf("an advisor with a goal: %v", e)
	}
	advisor, e := start(asSession(r.root), contract.SessionStartRequest{Name: "advisor", Advisor: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := start(ownerCaller, contract.SessionStartRequest{Name: "second", Advisor: true}); e == nil || !strings.Contains(e.Message, "already has an advisor, advisor") {
		t.Fatalf("a second advisor: %v", e)
	}
	if _, e := start(asSession(advisor.Session), contract.SessionStartRequest{Name: "helper"}); e == nil || !strings.Contains(e.Message, "starts no sessions") {
		t.Fatalf("the advisor's session start: %v", e)
	}
	if _, e := start(asSession(r.root), contract.SessionStartRequest{Name: "under", Parent: advisor.Session}); e == nil {
		t.Fatal("a session under the advisor started")
	}
	for name, caller := range map[string]contract.Principal{"advisor": asSession(advisor.Session), "root": asSession(r.root)} {
		if _, e := r.s.GoalSet(r.ctx, caller, contract.GoalSetRequest{Session: advisor.Session, Text: "work", DoneWhen: "done"}); e == nil || !strings.Contains(e.Message, "holds no goals") {
			t.Fatalf("goal set for the advisor by %s: %v", name, e)
		}
	}
	if _, e := r.s.GoalSet(r.ctx, asSession(advisor.Session), contract.GoalSetRequest{Text: "work", DoneWhen: "done"}); e == nil || !strings.Contains(e.Message, "holds no goals") {
		t.Fatalf("the advisor's own goal: %v", e)
	}
}

// advisorRig is a room with an advisor, and root's call to it running in the background.
type advisorRig struct {
	*goalRig
	advisor contract.SessionID
	answer  chan advisorResult
}

type advisorResult struct {
	response contract.AdvisorResponse
	problem  *contract.Error
}

func newAdvisorRig(t *testing.T) *advisorRig {
	r := &advisorRig{goalRig: newGoalRig(t), answer: make(chan advisorResult, 1)}
	started, e := r.s.SessionStart(r.ctx, asSession(r.root), contract.SessionStartRequest{Room: r.room, Name: "advisor", Profile: "test", Advisor: true})
	if e != nil {
		t.Fatal(e)
	}
	r.advisor = started.Session
	r.exec("UPDATE boxes SET state='ready',want='run'")
	return r
}

// ask is root's call, with a prompt, and waits until the advisor has the post.
func (r *advisorRig) ask(ctx context.Context) {
	go func() {
		response, problem := r.s.Advisor(ctx, asSession(r.root), contract.AdvisorRequest{Prompt: "Which cache?"})
		r.answer <- advisorResult{response, problem}
	}()
	for i := 0; i < 500; i++ {
		var posts int
		if err := r.st.Read().QueryRowContext(r.ctx, "SELECT COUNT(*) FROM deliveries WHERE to_kind='session' AND to_id=?", r.advisor).Scan(&posts); err != nil {
			r.t.Fatal(err)
		}
		if posts > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatal("the advisor was never posted to")
}

func (r *advisorRig) rootDeliveries() int {
	r.t.Helper()
	var n int
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT COUNT(*) FROM deliveries WHERE to_kind='session' AND to_id=?", r.root).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *advisorRig) result() advisorResult {
	r.t.Helper()
	select {
	case got := <-r.answer:
		return got
	case <-time.After(5 * time.Second):
		r.t.Fatal("the advisor call did not return")
		return advisorResult{}
	}
}

func TestAdvisorReturnsTheFinalTextOfItsTurn(t *testing.T) {
	r := newAdvisorRig(t)
	before := r.rootDeliveries()
	r.ask(r.ctx)
	r.turn(r.advisor, "Use the shared cache.")
	got := r.result()
	if got.problem != nil || got.response.Answer != "Use the shared cache." {
		t.Fatalf("answer: %+v", got)
	}
	var feed int
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT COUNT(*) FROM events e JOIN sessions s ON s.id=e.from_id WHERE e.type='post.created' AND e.from_kind='session' AND s.name IN ('root','advisor')").Scan(&feed); err != nil {
		t.Fatal(err)
	}
	if feed != 2 {
		t.Fatalf("%d posts from root and the advisor in the feed, want the prompt and the answer", feed)
	}
	if after := r.rootDeliveries(); after != before {
		t.Fatalf("root was also sent the answer as an input: %d deliveries, had %d", after, before)
	}
}

func TestAdvisorAnswersAnyOutcome(t *testing.T) {
	r := newAdvisorRig(t)
	r.ask(r.ctx)
	r.end(r.advisor, r.begin(r.advisor), contract.TurnFailed, "", "")
	got := r.result()
	if got.problem != nil || !strings.Contains(got.response.Answer, "failed") {
		t.Fatalf("answer to a failed turn: %+v", got)
	}
}

func TestAdvisorTimeoutLeavesTheReplyToArriveLater(t *testing.T) {
	r := newAdvisorRig(t)
	was := advisorTimeout
	advisorTimeout = 100 * time.Millisecond
	t.Cleanup(func() { advisorTimeout = was })
	before := r.rootDeliveries()
	r.ask(r.ctx)
	got := r.result()
	if got.problem == nil || !strings.Contains(got.problem.Message, "did not answer") || !strings.Contains(got.problem.Message, "arrive as an input") {
		t.Fatalf("timeout: %+v", got)
	}
	r.turn(r.advisor, "Late advice.")
	if after := r.rootDeliveries(); after != before+1 {
		t.Fatalf("root has %d deliveries, want the late reply on top of %d", after, before)
	}
}

func TestAdvisorCallEndsWhenRootsTurnStops(t *testing.T) {
	r := newAdvisorRig(t)
	ctx, stop := context.WithCancel(r.ctx)
	before := r.rootDeliveries()
	r.ask(ctx)
	stop()
	got := r.result()
	if got.problem == nil || !strings.Contains(got.problem.Message, "cancelled") {
		t.Fatalf("cancel: %+v", got)
	}
	r.turn(r.advisor, "Too late.")
	if after := r.rootDeliveries(); after != before+1 {
		t.Fatalf("root has %d deliveries, want the reply on top of %d", after, before)
	}
}

func TestAdvisorToolIsForRootWithAnAdvisor(t *testing.T) {
	r := newGoalRig(t)
	if _, e := r.s.Advisor(r.ctx, asSession(r.root), contract.AdvisorRequest{Prompt: "hi"}); e == nil || !strings.Contains(e.Message, "no advisor") {
		t.Fatalf("no advisor: %v", e)
	}
	if _, e := r.s.Advisor(r.ctx, asSession(r.lead), contract.AdvisorRequest{Prompt: "hi"}); e == nil || !strings.Contains(e.Message, "only root") {
		t.Fatalf("a lead: %v", e)
	}
	if _, e := r.s.Advisor(r.ctx, asSession(r.root), contract.AdvisorRequest{}); e == nil || !strings.Contains(e.Message, "prompt is required") {
		t.Fatalf("no prompt: %v", e)
	}
}

func TestAdvisorReadsRootsTranscript(t *testing.T) {
	r := newAdvisorRig(t)
	if _, e := r.s.TranscriptDigest(r.ctx, asSession(r.advisor), contract.TranscriptDigestRequest{Session: string(r.root)}); e != nil {
		t.Fatalf("the advisor reads root's transcript: %v", e)
	}
}
