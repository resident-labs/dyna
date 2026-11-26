package engine

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/live"
	"github.com/resident-labs/dyna/engine/prompt"
	"github.com/resident-labs/dyna/engine/store"
)

func TestBoxHelloGivesRootTheAdvisorSectionWhenTheRoomHasOne(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetLayout(box.Layout{Data: "/data", HostData: "/hostdata"})
	if err := s.store.SetEngineHost(ctx, "host", true); err != nil {
		t.Fatal(err)
	}
	plain, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{})
	if problem != nil || plain.Advisor || strings.Contains(plain.Instructions, "# Advisor") {
		t.Fatalf("root before an advisor: %v %+v", problem, plain.Advisor)
	}
	setAdvisorProfile(t, s, ctx, room, "test")
	passLifecycle(t, s, ctx)
	hello, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{})
	if problem != nil || !hello.Advisor {
		t.Fatalf("root hello: %v %+v", problem, hello.Advisor)
	}
	want := renderedHead(t, "root-advisor") + fmt.Sprintf("You are root of room room (ID %[1]s), session %[2]s.\nFiles: /box/shared is /data/rooms/%[1]s/shared/ on host.\nStanding rules: the global AGENTS.md is /data/AGENTS.md on host and this room's is /data/rooms/%[1]s/AGENTS.md; bash with host host edits them. The engine copies each room's file to every host for boxes and rewrites that copy at each sync.\nYour box has 1 CPU and 2 GiB of memory: heavy builds belong with an integrator or CI.", room, root)
	if hello.Instructions != want {
		t.Fatalf("root instructions:\n%s\nwant:\n%s", hello.Instructions, want)
	}
	setAdvisorProfile(t, s, ctx, room, "")
	if hello, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{}); problem != nil || hello.Advisor {
		t.Fatalf("root after none: %v %+v", problem, hello.Advisor)
	}
}

// setAdvisorProfile is the owner's room advisor setting.
func setAdvisorProfile(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, profile contract.ProfileName) {
	t.Helper()
	if _, e := s.RoomSet(ctx, ownerCaller, contract.RoomSetRequest{Room: room, Advisor: &profile}); e != nil {
		t.Fatal(e)
	}
	got, err := s.store.RoomAdvisorProfile(ctx, room)
	if err != nil || got != profile {
		t.Fatalf("the room's advisor is %q (%v), want %q", got, err, profile)
	}
}

func TestOnlyTheOwnerSetsTheAdvisor(t *testing.T) {
	r := newGoalRig(t)
	if _, e := r.s.RoomSet(r.ctx, asSession(r.root), contract.RoomSetRequest{Room: r.room, Advisor: new(contract.ProfileName)}); e == nil {
		t.Fatal("root set the advisor through room set")
	}
	setAdvisorProfile(t, r.s, r.ctx, r.room, "test")
	rooms, e := r.s.RoomList(r.ctx, ownerCaller, contract.RoomListRequest{})
	if e != nil || len(rooms.Rooms) != 1 || rooms.Rooms[0].Advisor != "test" {
		t.Fatalf("room list: %v %+v", e, rooms)
	}
}

// advisorRig is a room with an advisor whose root runs a turn, and a box channel standing in for root's box.
type advisorRig struct {
	*goalRig
	channel *live.Channel
	turnID  contract.TurnID
	answer  chan advisorResult
}

type advisorResult struct {
	response contract.AdvisorResponse
	problem  *contract.Error
}

func newAdvisorRig(t *testing.T) *advisorRig {
	r := &advisorRig{goalRig: newGoalRig(t), answer: make(chan advisorResult, 1)}
	setAdvisorProfile(t, r.s, r.ctx, r.room, "test")
	handle, _ := r.s.live.CreateSession(r.root)
	handle.Lock()
	r.channel = live.NewChannel(func() {})
	handle.SetChannel(r.channel)
	handle.Unlock()
	r.newRootTurn()
	return r
}

// newRootTurn ends root's running turn, if any, and starts another over an owner post.
func (r *advisorRig) newRootTurn() {
	r.t.Helper()
	if r.turnID != "" {
		r.end(r.root, r.turnID, contract.TurnCompleted, "", "done")
	}
	if _, problem := r.s.Post(r.ctx, ownerCaller, contract.PostRequest{Room: r.room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: r.root}, Text: "work"}); problem != nil {
		r.t.Fatal(problem)
	}
	r.turnID = r.begin(r.root)
}

// ask is root's call with a tool call ID, and returns what the box is asked.
func (r *advisorRig) ask(ctx context.Context, call string) contract.AgentTurn {
	r.t.Helper()
	go func() {
		response, problem := r.s.Advisor(ctx, asSession(r.root), contract.AdvisorRequest{Prompt: "Which cache?", ToolCallID: call})
		r.answer <- advisorResult{response, problem}
	}()
	select {
	case message := <-r.channel.Agents():
		return message.Body.(contract.AgentTurn)
	case <-time.After(5 * time.Second):
		r.t.Fatal("the box was never asked")
		return contract.AgentTurn{}
	}
}

// boxEnds is the box's answer for the agent.
func (r *advisorRig) boxEnds(id contract.AgentID, outcome contract.TurnOutcome, final string) {
	r.t.Helper()
	if _, e := r.s.AgentEnded(r.ctx, asSession(r.root), contract.AgentEndedRequest{ID: id, Outcome: outcome, Final: final, Usage: contract.Usage{Model: "m", InputTokens: 5}}); e != nil {
		r.t.Fatal(e)
	}
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

func (r *advisorRig) agentState(id contract.AgentID) string {
	r.t.Helper()
	agent, err := r.st.Agent(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return agent.State
}

func TestAdvisorAsksAnInBoxAgentAndReturnsItsFinalText(t *testing.T) {
	r := newAdvisorRig(t)
	turn := r.ask(r.ctx, "call_1")
	if turn.ID != "call_1" || turn.Name != "advisor" || !turn.Persist || !turn.Orient || turn.Instructions != prompt.AdvisorInstructions || turn.Profile.Name != "test" {
		t.Fatalf("the box was asked %+v", turn)
	}
	if !slices.Equal(turn.Tools, []string{"read", "grep", "find", "ls"}) {
		t.Fatalf("tools %v", turn.Tools)
	}
	if turn.Turn != r.turnID || turn.Delivery == 0 || !strings.HasSuffix(turn.Prompt, "Which cache?") || !strings.Contains(turn.Prompt, "Root's work since your last answer") || turn.FreshPrompt != "" {
		t.Fatalf("turn and prompt: %+v", turn)
	}
	if _, e := r.s.AgentStarted(r.ctx, asSession(r.root), contract.AgentStartedRequest{ID: "call_1", Transcript: "agents/advisor/a.jsonl", Offset: 0}); e != nil {
		t.Fatal(e)
	}
	r.boxEnds("call_1", contract.TurnCompleted, "Use the shared cache.")
	got := r.result()
	if got.problem != nil || got.response.Answer != "Use the shared cache." || got.response.Note != "" || got.response.Advisor != "test" {
		t.Fatalf("answer: %+v", got)
	}
	if state := r.agentState("call_1"); state != "completed" {
		t.Fatalf("the agent record is %q", state)
	}
	var feed int
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT COUNT(*) FROM events WHERE type='post.created' AND from_kind='session' AND home IS NULL").Scan(&feed); err != nil || feed != 0 {
		t.Fatalf("%d posts reached the feed: %v", feed, err)
	}
}

func TestASecondCallStartsItsRecordAfterTheFirstAndCarriesAFreshPrompt(t *testing.T) {
	r := newAdvisorRig(t)
	first := r.ask(r.ctx, "call_1")
	r.boxEnds(first.ID, contract.TurnCompleted, "one")
	r.result()
	r.newRootTurn()
	second := r.ask(r.ctx, "call_2")
	if second.FreshPrompt == "" || !strings.HasSuffix(second.FreshPrompt, "Which cache?") || second.Turn != r.turnID {
		t.Fatalf("second call %+v", second)
	}
	r.boxEnds(second.ID, contract.TurnCompleted, "two")
	if got := r.result(); got.response.Answer != "two" {
		t.Fatalf("answer %+v", got)
	}
}

func TestRootsStoppedTurnStopsTheAdvisorToo(t *testing.T) {
	r := newAdvisorRig(t)
	ctx, stop := context.WithCancel(r.ctx)
	r.ask(ctx, "call_1")
	stop()
	if _, ok := (<-r.channel.Agents()).Body.(contract.AgentStop); !ok {
		t.Fatal("the box was not told to stop")
	}
	r.boxEnds("call_1", contract.TurnStopped, "half")
	got := r.result()
	if got.problem != nil || got.response.Note != "root was stopped; the answer is what it had said so far" || got.response.Answer != "half" || r.agentState("call_1") != "stopped" {
		t.Fatalf("answer %+v", got)
	}
}

func TestAnAdvisorThatDoesNotAnswerTimesOutAndIsStopped(t *testing.T) {
	r := newAdvisorRig(t)
	timeout, wait := agentTimeout, agentStopWait
	agentTimeout, agentStopWait = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { agentTimeout, agentStopWait = timeout, wait })
	r.ask(r.ctx, "call_1")
	got := r.result()
	if got.problem != nil || !strings.Contains(got.response.Note, "timed out after") || got.response.Answer != "" {
		t.Fatalf("answer %+v", got)
	}
	if state := r.agentState("call_1"); state != "stopped" {
		t.Fatalf("the agent record is %q", state)
	}
}

func TestAnAdvisorTurnThatEndedWithoutTextSaysSo(t *testing.T) {
	r := newAdvisorRig(t)
	r.ask(r.ctx, "call_1")
	r.boxEnds("call_1", contract.TurnFailed, "")
	if got := r.result(); !strings.Contains(got.response.Note, "failed") {
		t.Fatalf("answer %+v", got)
	}
}

func TestOnlyTheSessionThatAskedMayAnswerAnAgent(t *testing.T) {
	r := newAdvisorRig(t)
	r.ask(r.ctx, "call_1")
	if _, e := r.s.AgentEnded(r.ctx, asSession(r.lead), contract.AgentEndedRequest{ID: "call_1", Outcome: contract.TurnCompleted, Final: "forged"}); e == nil {
		t.Fatal("another session ended the agent")
	}
	if _, e := r.s.AgentStarted(r.ctx, ownerCaller, contract.AgentStartedRequest{ID: "call_1", Transcript: "x.jsonl"}); e == nil {
		t.Fatal("the owner reported for a box")
	}
	r.boxEnds("call_1", contract.TurnCompleted, "real")
	if got := r.result(); got.response.Answer != "real" {
		t.Fatalf("answer %+v", got)
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
	setAdvisorProfile(t, r.s, r.ctx, r.room, "test")
	if _, e := r.s.Advisor(r.ctx, asSession(r.root), contract.AdvisorRequest{}); e == nil || !strings.Contains(e.Message, "prompt is required") {
		t.Fatalf("no prompt: %v", e)
	}
	if _, e := r.s.Advisor(r.ctx, asSession(r.root), contract.AdvisorRequest{Prompt: "hi"}); e == nil || !strings.Contains(e.Message, "no running turn") {
		t.Fatalf("no turn: %v", e)
	}
}

// An agent's steps lie in files under agents/ in root's transcript folder. They show for the agent and are not among root's own.
func TestAnAgentsStepsShowForItAndNotAmongTheSessionsOwn(t *testing.T) {
	s, ctx, root := transcriptCaller(t)
	session, err := s.store.Session(ctx, root.Session)
	if err != nil {
		t.Fatal(err)
	}
	wake, problem := s.Post(ctx, ownerCaller, contract.PostRequest{Room: session.Room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: root.Session}, Text: "wake"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.store.SeedEndedTurn(ctx, session.Room, root.Session, "turn_a", mustEventID(t, s, ctx, wake.Seq), wake.Seq, time.Unix(1, 0), time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	user := fmt.Sprintf(`{"type":"message","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","deliveryId":"%d"}}`+"\n", wake.Seq)
	call := func(id, command string) string {
		return fmt.Sprintf(`{"type":"message","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":%q,"name":"bash","arguments":{"command":%q}}]}}`+"\n", id, command)
	}
	if _, e := appendTranscript(s, ctx, root, "a.jsonl", 0, user+call("own", "root-step")); e != nil {
		t.Fatal(e)
	}
	// Two advisor calls in the one root turn, in one file.
	first := user + call("a1", "advisor-first")
	second := user + call("a2", "advisor-second")
	if _, e := appendTranscript(s, ctx, root, "agents/advisor/b.jsonl", 0, first+second); e != nil {
		t.Fatal(e)
	}
	for id, offset := range map[contract.AgentID]int64{"call_1": 0, "call_2": int64(len(first))} {
		if err := s.store.StartAgent(ctx, store.Agent{ID: id, Session: root.Session, Turn: "turn_a", Name: "advisor", Started: time.Unix(1, 0)}); err != nil {
			t.Fatal(err)
		}
		if err := s.store.AgentFile(ctx, root.Session, id, "agents/advisor/b.jsonl", offset); err != nil {
			t.Fatal(err)
		}
	}
	commands := func(req contract.TranscriptReadRequest) string {
		read, e := s.TranscriptRead(ctx, root, req)
		if e != nil {
			t.Fatal(e)
		}
		var out string
		for _, turn := range read.Turns {
			for _, item := range turn.Items {
				out += item.Arguments + "\n"
			}
		}
		return out
	}
	own := commands(contract.TranscriptReadRequest{Session: root.Session})
	if !strings.Contains(own, "root-step") || strings.Contains(own, "advisor") {
		t.Fatalf("the session's own steps:\n%s", own)
	}
	one := commands(contract.TranscriptReadRequest{Session: root.Session, Agent: "call_1"})
	if !strings.Contains(one, "advisor-first") || strings.Contains(one, "advisor-second") || strings.Contains(one, "root-step") {
		t.Fatalf("the first call's steps:\n%s", one)
	}
	two := commands(contract.TranscriptReadRequest{Session: root.Session, Agent: "call_2"})
	if !strings.Contains(two, "advisor-second") || strings.Contains(two, "advisor-first") {
		t.Fatalf("the second call's steps:\n%s", two)
	}
	if _, e := s.TranscriptRead(ctx, root, contract.TranscriptReadRequest{Session: root.Session, Agent: "nonesuch"}); e == nil {
		t.Fatal("an unknown agent was read")
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
	if !strings.Contains(first, strings.Repeat("y", 300)) || strings.Contains(first, strings.Repeat("y", 301)) {
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
	for i := calls - advisorMaxSteps; i < calls; i++ {
		if !strings.Contains(later, fmt.Sprintf("step-%d-end", i)) {
			t.Fatalf("a call on a later turn lost step %d of the newest %d", i, advisorMaxSteps)
		}
	}
	if strings.Contains(later, "step-569-end") || !strings.Contains(later, "570 earlier steps omitted; transcript read has them") {
		t.Fatalf("the step cap did not hold or did not say so:\n%s", later)
	}
}

func TestAdvisorDeltaStaysWithinTheCharacterCap(t *testing.T) {
	s, ctx, root := transcriptCaller(t)
	session, err := s.store.Session(ctx, root.Session)
	if err != nil {
		t.Fatal(err)
	}
	wake, problem := s.Post(ctx, ownerCaller, contract.PostRequest{Room: session.Room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: root.Session}, Text: "wake"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.store.SeedEndedTurn(ctx, session.Room, root.Session, "turn_a", mustEventID(t, s, ctx, wake.Seq), wake.Seq, time.Unix(1, 0), time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"type":"message","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","deliveryId":"%d"}}`+"\n", wake.Seq)
	for i := 0; i < 20; i++ {
		data += fmt.Sprintf(`{"type":"message","timestamp":"2026-01-01T00:00:%02dZ","message":{"role":"assistant","content":[{"type":"text","text":"n%02d-%s"}]}}`+"\n", i, i, strings.Repeat("x", 5000))
	}
	if _, e := appendTranscript(s, ctx, root, "a.jsonl", 0, data); e != nil {
		t.Fatal(e)
	}
	delta, e := s.advisorDelta(ctx, root.Session, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if e != nil {
		t.Fatal(e)
	}
	// Each text is clipped to 2,000 characters, so 24,000 holds the newest 11 of the 20.
	if len(delta) > advisorMaxChars+200 || !strings.Contains(delta, "n19-") || strings.Contains(delta, "n08-") || !strings.Contains(delta, "9 earlier steps omitted; transcript read has them") {
		t.Fatalf("%d characters:\n%.300s", len(delta), delta)
	}
}

func mustEventID(t *testing.T, s *Service, ctx context.Context, seq int64) contract.EventID {
	id, err := s.store.EventID(ctx, seq)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The engine restarts while the advisor runs: root's call died with it, so no call waits. The box's reconnect stops the agent and ends its
// row, and the owner's Stop reaches an agent by its row.
func TestAnAgentWhoseCallDiedWithTheEngineIsStoppedWhenItsBoxReconnects(t *testing.T) {
	r := newAdvisorRig(t)
	turn := r.ask(r.ctx, "call_1")
	if turn.TimeoutMS != agentTimeout.Milliseconds() {
		t.Fatalf("the box was given no timeout: %+v", turn)
	}
	r.restart()
	handle, _ := r.s.live.CreateSession(r.root)
	handle.Lock()
	r.channel = live.NewChannel(func() {})
	handle.SetChannel(r.channel)
	handle.Unlock()
	if state := r.agentState("call_1"); state != "running" {
		t.Fatalf("after the restart the row is %q", state)
	}
	r.s.stopOrphanedAgents(r.ctx, r.root)
	select {
	case message := <-r.channel.Agents():
		if stop, ok := message.Body.(contract.AgentStop); !ok || stop.ID != "call_1" {
			t.Fatalf("the box was sent %+v", message.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the box was never told to stop")
	}
	if state := r.agentState("call_1"); state != "stopped" {
		t.Fatalf("the row is %q", state)
	}
}

// The CLI passes `dyna room advisor ROOM PROFILE` what the owner typed, a room's name as often as its ID.
func TestARoomSettingTakesTheRoomsNameOrID(t *testing.T) {
	r := newGoalRig(t)
	name, err := r.st.RoomNameOf(r.ctx, r.room)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []contract.RoomID{contract.RoomID(name), r.room} {
		profile := contract.ProfileName("test")
		if _, e := r.s.RoomSet(r.ctx, ownerCaller, contract.RoomSetRequest{Room: ref, Advisor: &profile}); e != nil {
			t.Fatalf("room %q: %v", ref, e)
		}
		if got, err := r.st.RoomAdvisorProfile(r.ctx, r.room); err != nil || got != profile {
			t.Fatalf("room %q: the advisor is %q (%v)", ref, got, err)
		}
		none := contract.ProfileName("")
		if _, e := r.s.RoomSet(r.ctx, ownerCaller, contract.RoomSetRequest{Room: ref, Advisor: &none}); e != nil {
			t.Fatal(e)
		}
	}
	profile := contract.ProfileName("test")
	if _, e := r.s.RoomSet(r.ctx, ownerCaller, contract.RoomSetRequest{Room: "nowhere", Advisor: &profile}); e == nil || !strings.Contains(e.Message, "unknown room") {
		t.Fatalf("an unknown room: %v", e)
	}
}
