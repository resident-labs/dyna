package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
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
	advisor := setAdvisorProfile(t, s, ctx, room, "test")
	passLifecycle(t, s, ctx)
	hello, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{})
	if problem != nil || !hello.Advisor {
		t.Fatalf("root hello: %v %+v", problem, hello.Advisor)
	}
	want := renderedHead(t, "root-advisor") + fmt.Sprintf("You are root of room room (ID %[1]s), session %[2]s.\nFiles: /box/shared is /data/rooms/%[1]s/shared/ on host.\nStanding rules: the global AGENTS.md is /data/AGENTS.md on host and this room's is /data/rooms/%[1]s/AGENTS.md; bash with host host edits them. The engine copies each room's file to every host for boxes and rewrites that copy at each sync.\nYour box has 1 CPU and 2 GiB of memory: heavy builds belong in a larger child session or CI.", room, root)
	if hello.Instructions != want {
		t.Fatalf("root instructions:\n%s\nwant:\n%s", hello.Instructions, want)
	}
	adviser, problem := s.BoxHello(ctx, boxCaller(advisor), contract.BoxHelloRequest{})
	if problem != nil || adviser.Advisor || adviser.Root {
		t.Fatalf("advisor hello: %v %+v", problem, adviser)
	}
	want = renderedHead(t, "advisor") + fmt.Sprintf("You are advisor, session %s, in room room (ID %s). Your parent is root.", advisor, room) + sessionBox
	if adviser.Instructions != want {
		t.Fatalf("advisor instructions:\n%s\nwant:\n%s", adviser.Instructions, want)
	}
}

// setAdvisorProfile is the owner's room advisor setting, and returns the advisor session it leaves, or "" for none.
func setAdvisorProfile(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, profile contract.ProfileName) contract.SessionID {
	t.Helper()
	if _, e := s.RoomSet(ctx, ownerCaller, contract.RoomSetRequest{Room: room, Advisor: &profile}); e != nil {
		t.Fatal(e)
	}
	advisor, err := s.store.RoomAdvisor(ctx, room)
	if errors.Is(err, store.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return advisor.ID
}

func TestAdvisorSettingStartsSwitchesAndClosesTheSession(t *testing.T) {
	r := newGoalRig(t)
	if _, e := r.s.RoomSet(r.ctx, asSession(r.root), contract.RoomSetRequest{Room: r.room, Advisor: new(contract.ProfileName)}); e == nil {
		t.Fatal("root set the advisor through room set")
	}
	advisor := setAdvisorProfile(t, r.s, r.ctx, r.room, "test")
	if advisor == "" {
		t.Fatal("setting a profile started no advisor")
	}
	var parent, profile string
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT parent,profile FROM sessions WHERE id=?", advisor).Scan(&parent, &profile); err != nil || parent != string(r.root) || profile != "test" {
		t.Fatalf("advisor under %q on %q: %v", parent, profile, err)
	}
	if again := setAdvisorProfile(t, r.s, r.ctx, r.room, "test"); again != advisor {
		t.Fatalf("setting the same profile made %s, want %s", again, advisor)
	}
	if same := setAdvisorProfile(t, r.s, r.ctx, r.room, "deep"); same != advisor {
		t.Fatalf("a new profile made %s, want the same session %s", same, advisor)
	}
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT profile FROM sessions WHERE id=?", advisor).Scan(&profile); err != nil || profile != "deep" {
		t.Fatalf("profile %q: %v", profile, err)
	}
	if none := setAdvisorProfile(t, r.s, r.ctx, r.room, ""); none != "" {
		t.Fatalf("none left advisor %s", none)
	}
	hello, e := r.s.BoxHello(r.ctx, boxCaller(r.root), contract.BoxHelloRequest{})
	if e != nil || hello.Advisor || strings.Contains(hello.Instructions, "# Advisor\n\nYou have an advisor") {
		t.Fatalf("root after none: %v advisor=%v", e, hello.Advisor)
	}
}

func TestAdvisorHoldsNothingAndStartsNothing(t *testing.T) {
	r := newGoalRig(t)
	advisor := setAdvisorProfile(t, r.s, r.ctx, r.room, "test")
	if _, e := r.s.SessionStart(r.ctx, asSession(advisor), contract.SessionStartRequest{Room: r.room, Name: "helper", Profile: "test"}); e == nil || !strings.Contains(e.Message, "starts no sessions") {
		t.Fatalf("the advisor's session start: %v", e)
	}
	if _, e := r.s.SessionStart(r.ctx, asSession(r.root), contract.SessionStartRequest{Room: r.room, Name: "under", Profile: "test", Parent: advisor}); e == nil {
		t.Fatal("a session under the advisor started")
	}
	for name, caller := range map[string]contract.Principal{"advisor": asSession(advisor), "root": asSession(r.root)} {
		if _, e := r.s.GoalSet(r.ctx, caller, contract.GoalSetRequest{Session: advisor, Text: "work", DoneWhen: "done"}); e == nil || !strings.Contains(e.Message, "holds no goals") {
			t.Fatalf("goal set for the advisor by %s: %v", name, e)
		}
	}
	if _, e := r.s.GoalSet(r.ctx, asSession(advisor), contract.GoalSetRequest{Text: "work", DoneWhen: "done"}); e == nil || !strings.Contains(e.Message, "holds no goals") {
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
	r.advisor = setAdvisorProfile(t, r.s, r.ctx, r.room, "test")
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
	var text, context string
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT json_extract(CAST(body AS TEXT),'$.text'),coalesce(json_extract(CAST(body AS TEXT),'$.context'),'') FROM events e JOIN sessions s ON s.id=e.from_id WHERE e.type='post.created' AND s.name='root' ORDER BY e.seq DESC LIMIT 1").Scan(&text, &context); err != nil || text != "Which cache?" || context == "" {
		t.Fatalf("the feed post is %q with context %q (%v): want the bare prompt, and the delta kept for the advisor", text, context, err)
	}
	body, err := room.RenderDelivery(contract.PostCreated{Text: text, Context: context})
	if err != nil || !strings.HasPrefix(body, context) || !strings.HasSuffix(body, "Which cache?") {
		t.Fatalf("the advisor reads %q (%v)", body, err)
	}
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

func TestRootKeepsAdvisorToolAndSectionWhileItsBoxIsStopped(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	advisor := setAdvisorProfile(t, s, ctx, room, "test")
	passLifecycle(t, s, ctx)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='stopped',want='stop' WHERE session=?", advisor)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	hello, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{})
	if problem != nil || !hello.Advisor || !strings.Contains(hello.Instructions, "# Advisor\n\nYou have an advisor") {
		t.Fatalf("root with a stopped advisor: %v advisor=%v", problem, hello.Advisor)
	}
}

func TestIdleSweepsNeverCloseTheAdvisor(t *testing.T) {
	r := newAdvisorRig(t)
	plain, e := r.s.SessionStart(r.ctx, asSession(r.root), contract.SessionStartRequest{Room: r.room, Name: "plain", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	r.exec("UPDATE boxes SET state='stopped',want='stop',updated_at=1")
	ids, err := r.st.DormantCloseCandidates(r.ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, plain.Session) || slices.Contains(ids, r.advisor) {
		t.Fatalf("candidates %v: want the plain child %s and not the advisor %s", ids, plain.Session, r.advisor)
	}
}

func TestAdvisorDeltaCarriesOnlyNewStepsAndCutsLongResults(t *testing.T) {
	s, ctx, root := transcriptCaller(t)
	session, err := s.store.Session(ctx, root.Session)
	if err != nil {
		t.Fatal(err)
	}
	wake, problem := s.Post(ctx, ownerCaller, contract.PostRequest{Room: session.Room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: root.Session}, Text: "wake"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		var input contract.EventID
		if err := tx.QueryRowContext(ctx, "SELECT id FROM events WHERE seq=?", wake.Seq).Scan(&input); err != nil {
			return err
		}
		event := contract.Event{ID: "delta-start", Room: session.Room, From: root, Time: time.Unix(1, 0), Body: contract.TurnStarted{Session: root.Session, Turn: "turn_a", Inputs: []contract.EventID{input}}}
		seq, err := s.store.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('turn_a',?,0,'','',0,0,0,0,?)", root.Session, seq); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE deliveries SET turn='turn_a' WHERE to_kind='session' AND to_id=? AND seq=?", root.Session, wake.Seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	record := func(at, role, content string) string {
		return fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":%q,"content":[%s]}}`+"\n", at, role, content)
	}
	data := fmt.Sprintf(`{"type":"message","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","deliveryId":"%d"}}`+"\n", wake.Seq) +
		record("2026-01-01T00:00:01Z", "assistant", `{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"first"}}`) +
		record("2026-01-01T00:00:02Z", "toolResult", "") +
		record("2026-01-01T00:00:10Z", "assistant", `{"type":"toolCall","id":"c2","name":"bash","arguments":{"command":"second"}}`)
	data = strings.Replace(data, `"role":"toolResult","content":[]`, fmt.Sprintf(`"role":"toolResult","toolCallId":"c1","content":[{"type":"text","text":%q}]`, strings.Repeat("y", 3000)), 1)
	if _, e := appendTranscript(s, ctx, root, "a.jsonl", 0, data); e != nil {
		t.Fatal(e)
	}
	first, e := s.advisorDelta(ctx, root.Session, time.Time{})
	if e != nil || !strings.Contains(first, "first") || !strings.Contains(first, "second") {
		t.Fatalf("first call: %v\n%s", e, first)
	}
	if !strings.Contains(first, "(cut; transcript read has the rest)") || strings.Contains(first, strings.Repeat("y", 2001)) {
		t.Fatalf("a long result was not cut:\n%s", first)
	}
	later, e := s.advisorDelta(ctx, root.Session, time.Date(2026, 1, 1, 0, 0, 5, 0, time.UTC))
	if e != nil || strings.Contains(later, "first") || !strings.Contains(later, "second") {
		t.Fatalf("later call: %v\n%s", e, later)
	}
}

func TestAdvisorDeltaReadsAllOfALongTurn(t *testing.T) {
	s, ctx, root := transcriptCaller(t)
	session, err := s.store.Session(ctx, root.Session)
	if err != nil {
		t.Fatal(err)
	}
	wake, problem := s.Post(ctx, ownerCaller, contract.PostRequest{Room: session.Room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: root.Session}, Text: "wake"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		var input contract.EventID
		if err := tx.QueryRowContext(ctx, "SELECT id FROM events WHERE seq=?", wake.Seq).Scan(&input); err != nil {
			return err
		}
		seq, err := s.store.AppendEvent(tx, contract.Event{ID: "long-start", Room: session.Room, From: root, Time: time.Unix(1, 0), Body: contract.TurnStarted{Session: root.Session, Turn: "turn_a", Inputs: []contract.EventID{input}}})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('turn_a',?,0,'','',0,0,0,0,?)", root.Session, seq); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE deliveries SET turn='turn_a' WHERE to_kind='session' AND to_id=? AND seq=?", root.Session, wake.Seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	const calls = 600 // more than one page of items
	data := fmt.Sprintf(`{"type":"message","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","deliveryId":"%d"}}`+"\n", wake.Seq)
	for i := 0; i < calls; i++ {
		data += fmt.Sprintf(`{"type":"message","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c%d","name":"bash","arguments":{"command":"step-%d-end"}}]}}`+"\n", i, i)
	}
	if _, e := appendTranscript(s, ctx, root, "a.jsonl", 0, data); e != nil {
		t.Fatal(e)
	}
	delta, e := s.advisorDelta(ctx, root.Session, time.Time{})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < calls; i++ {
		if !strings.Contains(delta, fmt.Sprintf("step-%d-end", i)) && i >= calls-advisorFirstSteps {
			t.Fatalf("step %d missing from the last %d", i, advisorFirstSteps)
		}
	}
	later, e := s.advisorDelta(ctx, root.Session, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < calls; i++ {
		if !strings.Contains(later, fmt.Sprintf("step-%d-end", i)) {
			t.Fatalf("a call on a later turn lost step %d of %d", i, calls)
		}
	}
}
