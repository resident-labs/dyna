package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func (e *testEnv) human(rec room.Record, text string, mentions ...string) Result {
	e.t.Helper()
	out, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderOwner, Room: rec.Name, Human: true}, Mentions: mentions, Text: text})
	if err != nil {
		e.t.Fatalf("human post: %v", err)
	}
	return out
}

func (e *testEnv) rootSession(rec room.Record) subagent.Info {
	e.t.Helper()
	info, ok, err := e.mgr.RootSession(rec.ID)
	if err != nil || !ok {
		e.t.Fatalf("RootSession = %+v, %v, %v", info, ok, err)
	}
	return info
}

// turnInput is a turn's prompt and the posts drained into it.
func (e *testEnv) turnInput(id string, n int) string {
	e.t.Helper()
	turn, err := e.mgr.Store.ReadTurn(id, n)
	if err != nil {
		e.t.Fatal(err)
	}
	out := turn.Prompt
	for _, st := range turn.Steering {
		out += "\n" + st.ID + "\n" + st.Message
	}
	return out
}

func rootDelivery(out Result, rec room.Record) string {
	for _, d := range out.Deliveries {
		if d.To == rec.Name+":"+room.RootAgent {
			return d.Delivery
		}
	}
	return ""
}

// The human's first post to @root starts the internal root with that post as
// its input. Its own turn report never wakes it. A top-level session's report
// remains in the feed without waking root. Failures still wake root.
func TestHumanPostStartsRootAndOnlyFailuresWakeIt(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	agent := e.start(rec, "agent", nil)
	if got := rootDelivery(e.human(rec, "RESPOND: hello", "@agent"), rec); got != "" {
		t.Fatalf("a post to an agent reached the internal root: %q", got)
	}
	if _, ok, _ := e.mgr.RootSession(rec.ID); ok {
		t.Fatal("a post to an agent started the internal root")
	}
	if got := rootDelivery(e.human(rec, "RESPOND: first answer", "@root"), rec); got != "root-started" {
		t.Fatalf("root delivery = %q, want root-started", got)
	}
	root := e.rootSession(rec)
	if root.Role != subagent.RoleRoot || root.EffectiveName() != room.RootAgent || root.ParentSession != "" {
		t.Fatalf("internal root = %+v", root.Meta)
	}
	root = e.wait(root.ID, 1)
	if p := e.turnInput(root.ID, 1); !strings.Contains(p, "RESPOND: first answer") {
		t.Fatalf("first turn prompt = %q", p)
	}
	answer, err := e.svc.CompleteSessionTurn(e.ctx, root.ID, 1)
	if err != nil || answer.Message.From != rec.Name+":"+room.RootAgent || !strings.Contains(answer.Message.Text, "first answer") || !answer.Message.Addressed(rec.Name) {
		t.Fatalf("the internal root's answer did not reach the room: %+v, %v", answer.Message, err)
	}
	if got := rootDelivery(answer, rec); got != "" {
		t.Fatalf("the internal root's own report was delivered to it: %q", got)
	}
	if root = e.rootSession(rec); root.Turns != 1 {
		t.Fatalf("the internal root's own report woke it: %d turns", root.Turns)
	}
	// A later human question owes an answer too, and that answer does not
	// wake the internal root again.
	if got := rootDelivery(e.human(rec, "RESPOND: second answer", "@root"), rec); got != "session-queued" {
		t.Fatalf("second question delivery = %q", got)
	}
	e.wait(root.ID, 2)
	second, err := e.svc.CompleteSessionTurn(e.ctx, root.ID, 2)
	if err != nil || second.Message.From != rec.Name+":"+room.RootAgent || !strings.Contains(second.Message.Text, "second answer") || rootDelivery(second, rec) != "" {
		t.Fatalf("the second answer did not reach the room: %+v, %v", second, err)
	}
	if root = e.rootSession(rec); root.Turns != 2 {
		t.Fatalf("the second answer woke the internal root: %d turns", root.Turns)
	}
	report, err := e.svc.CompleteSessionTurn(e.ctx, agent.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Message.OwnerReport || rootDelivery(report, rec) != "" {
		t.Fatalf("top-level report = %+v", report)
	}
	if _, err := e.mgr.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.rootSession(rec); got.Turns != 2 {
		t.Fatalf("successful report woke root: %+v", got)
	}
	failed := e.start(rec, "failed", nil)
	if _, err := e.mgr.Store.UpdateTurn(failed.ID, 1, func(turn *subagent.Turn) {
		turn.Status, turn.Error = subagent.TurnError, "provider unavailable"
		turn.PostedTo = []string{rec.Name}
	}); err != nil {
		t.Fatal(err)
	}
	failure, err := e.svc.CompleteSessionTurn(e.ctx, failed.ID, 1)
	if err != nil || rootDelivery(failure, rec) != "session-queued" {
		t.Fatalf("failure did not wake root: %+v %v", failure, err)
	}
	e.wait(root.ID, 3)
	if p := e.turnInput(root.ID, 3); !strings.Contains(p, failure.Message.ID) {
		t.Fatalf("third turn input misses failure %s: %q", failure.Message.ID, p)
	}
}

// While an external orchestrator holds the seat, posts to @root do not reach
// the internal root, and after detach they are never replayed. The internal
// root's next input is a seat note, then new posts.
func TestExternalSeatHoldsPostsAndDetachSkipsThem(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	e.human(rec, "RESPOND: started", "@root")
	root := e.wait(e.rootSession(rec).ID, 1)
	if _, _, err := e.svc.Rooms.SetSeat(e.ctx, rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	held := e.human(rec, "RESPOND: for the external orchestrator", "@root")
	if got := rootDelivery(held, rec); got != "" {
		t.Fatalf("attached seat delivered to the internal root: %q", got)
	}
	rec, _ = e.svc.Rooms.Resolve(rec.ID)
	seat, changed, deliveries, err := e.svc.ReturnSeat(e.ctx, rec, "")
	if err != nil || !changed || seat.External || len(deliveries) != 0 {
		t.Fatalf("ReturnSeat = %+v, %v, %+v, %v", seat, changed, deliveries, err)
	}
	if got := e.rootSession(rec); got.Turns != 1 {
		t.Fatalf("detach without a handoff woke the internal root: %d turns", got.Turns)
	}
	if got := rootDelivery(e.human(rec, "RESPOND: back", "@root"), rec); got != "session-queued" {
		t.Fatalf("root delivery after detach = %q", got)
	}
	e.wait(root.ID, 2)
	p := e.turnInput(root.ID, 2)
	if !strings.Contains(p, "held the root seat from") || !strings.Contains(p, "RESPOND: back") || strings.Contains(p, held.Message.ID) {
		t.Fatalf("turn after detach = %q", p)
	}
}

// A handoff becomes the internal root's next input. With no internal root
// open, the handoff starts one.
func TestHandoffStartsTheInternalRoot(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	if _, _, err := e.svc.Rooms.SetSeat(e.ctx, rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	rec, _ = e.svc.Rooms.Resolve(rec.ID)
	note := "PR 12 waits for CI. " + strings.Repeat("full external root handoff evidence ", 1500)
	_, _, deliveries, err := e.svc.ReturnSeat(e.ctx, rec, note)
	if err != nil || len(deliveries) != 1 || deliveries[0].Delivery != "root-started" {
		t.Fatalf("ReturnSeat deliveries = %+v, %v", deliveries, err)
	}
	root := e.wait(e.rootSession(rec).ID, 1)
	if p := e.turnInput(root.ID, 1); !strings.Contains(p, "[ROOT SEAT HANDOFF]") || !strings.Contains(p, "Review this source before acting") || !strings.Contains(p, "root-handoff-rest.txt") || !strings.Contains(p, note[:1000]) {
		t.Fatalf("handoff must reference its full note: %q", p)
	}
	messages, err := e.svc.Rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := messages[0]
	for _, msg := range messages {
		if msg.Kind == "seat" && len(msg.Attachments) == 1 {
			last = msg
		}
	}
	if len(last.Attachments) != 1 {
		t.Fatal("full handoff attachment missing")
	}
	path, err := e.svc.Rooms.AttachmentPath(rec.ID, last.Attachments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(last.Text, note[:1000])
	if start < 0 {
		t.Fatal("written handoff prefix missing")
	}
	inline, _, ok := strings.Cut(last.Text[start:], "\n\n[Written handoff continues")
	if !ok || inline+string(saved) != note || string(saved) == note || !utf8.ValidString(inline) || !utf8.Valid(saved) {
		t.Fatal("inline handoff and attached remainder do not preserve the note")
	}
}

// Returning an existing root receives only stored input until the engine runs.
func TestHandoffQueuesExistingRootWithoutEngine(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	e.human(rec, "RESPOND: started", "@root")
	root := e.wait(e.rootSession(rec).ID, 1)
	if _, _, err := e.svc.Rooms.SetSeat(e.ctx, rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	rec, _ = e.svc.Rooms.Resolve(rec.ID)
	_, _, deliveries, err := e.svc.ReturnSeat(e.ctx, rec, "RESPOND: returned")
	if err != nil || len(deliveries) != 1 || deliveries[0].Delivery != "session-queued" {
		t.Fatalf("ReturnSeat deliveries: %+v, %v", deliveries, err)
	}
	if got := e.rootSession(rec); got.Turns != 1 {
		t.Fatalf("handoff poster started turn %d", got.Turns)
	}
	if _, err := e.mgr.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.rootSession(rec); got.Turns != 2 {
		t.Fatalf("engine missed handoff: %d turns", got.Turns)
	}
	e.wait(root.ID, 2)
	if input := e.turnInput(root.ID, 2); !strings.Contains(input, "[ROOT SEAT HANDOFF]") || !strings.Contains(input, "RESPOND: returned") {
		t.Fatalf("handoff input: %q", input)
	}
}

func TestFirstRootPostIsClaimedAndDelivered(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	const request = "first-owner-request-claim"
	out := e.human(rec, "SLEEP:0\nRESPOND: "+request, "@root")
	root := e.rootSession(rec)
	e.wait(root.ID, 1)
	// Reproduce the same-millisecond creation boundary without a clock race.
	if _, err := e.mgr.Store.UpdateMeta(root.ID, func(meta *subagent.Meta) {
		meta.Created = time.UnixMilli(out.Message.TS)
	}); err != nil {
		t.Fatal(err)
	}
	queue, err := e.mgr.Store.SessionQueue(e.svc.Rooms, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range queue {
		if item.ID == out.Message.ID {
			found = true
			if item.State != "delivered" || item.Turn != 1 || item.Cancellable {
				t.Fatalf("first root post is not delivered: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("first root post missing from queue")
	}
	// A recorded receipt also keeps the input visible if creation happened
	// in a later millisecond than the post.
	if _, err := e.mgr.Store.UpdateMeta(root.ID, func(meta *subagent.Meta) {
		meta.Created = time.UnixMilli(out.Message.TS + 1)
	}); err != nil {
		t.Fatal(err)
	}
	queue, err = e.mgr.Store.SessionQueue(e.svc.Rooms, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, item := range queue {
		if item.ID == out.Message.ID && item.State == "delivered" {
			found = true
		}
	}
	if !found {
		t.Fatal("creation-time filter hid the recorded first-post receipt")
	}
	turn, err := e.mgr.Store.ReadTurn(root.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	delivered := 0
	for _, steer := range turn.Steering {
		if steer.ID == out.Message.ID && steer.Delivered {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("first post receipts: %+v", turn.Steering)
	}
	// The queue's row is the truth; queue.json is only the earlier release's
	// copy.
	db, err := database.Open(filepath.Dir(e.mgr.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	if err := db.QueryRow("SELECT queue FROM session_queues WHERE session = ?", root.ID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Claimed map[string]int `json:"claimed"`
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Claimed[out.Message.ID] != 1 {
		t.Fatalf("first post claim: %+v", claims.Claimed)
	}
	if count := strings.Count(e.turnInput(root.ID, 1), request); count != 1 {
		t.Fatalf("first input appears %d times", count)
	}
	if err := e.mgr.TickSessionTargets(e.ctx, []string{root.ID}); err != nil {
		t.Fatal(err)
	}
	meta, err := e.mgr.Store.ReadMeta(root.ID)
	if err != nil || meta.Turns != 1 {
		t.Fatalf("delivered first post was replayed: %+v %v", meta, err)
	}
}

// An owner DM to an agent never wakes the internal root. Its next turn gets
// one short block with the DM's start and id, and the agent's report ids.
func TestOwnerDMsReachRootAtItsNextTurnWithoutWaking(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	agent := e.start(rec, "agent", nil)
	e.human(rec, "RESPOND: first", "@root")
	root := e.wait(e.rootSession(rec).ID, 1)
	if _, err := e.svc.CompleteSessionTurn(e.ctx, root.ID, 1); err != nil {
		t.Fatal(err)
	}
	dm := e.human(rec, "Go ahead and delete the old voice files. "+strings.Repeat("x", 300), "@agent")
	if got := rootDelivery(dm, rec); got != "" {
		t.Fatalf("an owner DM reached the internal root: %q", got)
	}
	report, err := e.svc.CompleteSessionTurn(e.ctx, agent.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.rootSession(rec); got.Turns != 1 {
		t.Fatalf("an owner DM started a root turn: %d turns", got.Turns)
	}
	e.human(rec, "RESPOND: second", "@root")
	e.wait(root.ID, 2)
	// The mock echoes everything after RESPOND:, and the block follows the posts.
	answer, err := e.svc.CompleteSessionTurn(e.ctx, root.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	input := answer.Message.Text
	for _, want := range []string{"Owner DMs since your last turn", "@agent: Go ahead and delete the old voice files.", dm.Message.ID, report.Message.ID} {
		if !strings.Contains(input, want) {
			t.Fatalf("second turn input misses %q:\n%s", want, input)
		}
	}
	if strings.Contains(input, strings.Repeat("x", 250)) {
		t.Fatalf("the block carries more than the DM's start:\n%s", input)
	}
}

func TestRootDetachKeepsWrittenNoteBelowPostLimit(t *testing.T) {
	rootDetachBoundary(t, false)
}

func TestRootDetachSplitsWrittenNoteAbovePostLimit(t *testing.T) {
	rootDetachBoundary(t, true)
}

func rootDetachBoundary(t *testing.T, overflow bool) {
	t.Helper()
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	now := time.Date(2026, 10, 21, 6, 0, 0, 0, time.UTC)
	e.svc.Rooms.Now = func() time.Time { return now }
	detach := func(note string) room.Message {
		t.Helper()
		if _, _, err := e.svc.Rooms.SetSeat(e.ctx, rec.ID, room.Seat{External: true}, nil); err != nil {
			t.Fatal(err)
		}
		rec, _ = e.svc.Rooms.Resolve(rec.ID)
		if _, _, _, err := e.svc.ReturnSeat(e.ctx, rec, note); err != nil {
			t.Fatal(err)
		}
		root := e.rootSession(rec)
		if _, err := e.mgr.TickSessions(e.ctx); err != nil {
			t.Fatal(err)
		}
		latest, err := e.mgr.Store.Info(root.ID)
		if err != nil {
			t.Fatal(err)
		}
		e.wait(root.ID, latest.Turns)
		messages, err := e.svc.Rooms.Messages(rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Kind == "session" && messages[i].Notice.Kind == "seat" {
				return messages[i]
			}
		}
		t.Fatal("seat handoff post missing")
		return room.Message{}
	}
	pilot := strings.Repeat("x", 500)
	first := detach(pilot)
	if !strings.Contains(first.Text, pilot) || len(first.Attachments) != 0 {
		t.Fatal("small written note must stay inline")
	}
	header := len(first.Text) - len(pilot)
	capacity := room.MaxMessageBytes - header
	note := strings.Repeat("x", capacity-1)
	if overflow {
		note += "🌱"
	}
	posted := detach(note)
	if len(posted.Text) > room.MaxMessageBytes || !utf8.ValidString(posted.Text) {
		t.Fatal("detach exceeded the post byte limit or split UTF-8")
	}
	if !overflow {
		if len(posted.Text) != room.MaxMessageBytes-1 || !strings.Contains(posted.Text, note) || len(posted.Attachments) != 0 {
			t.Fatal("fitting written note was not kept inline")
		}
		return
	}
	if len(posted.Attachments) != 1 {
		t.Fatal("overflow needs one remainder attachment")
	}
	path, err := e.svc.Rooms.AttachmentPath(rec.ID, posted.Attachments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(posted.Text, pilot)
	if start < 0 {
		t.Fatal("overflow lost the inline note prefix")
	}
	inline, _, ok := strings.Cut(posted.Text[start:], "\n\n[Written handoff continues")
	if !ok || inline+string(rest) != note || !utf8.Valid(rest) || len(rest) >= len(note) {
		t.Fatal("overflow did not preserve exactly the note remainder")
	}
}

// Unmarked worker copies stay out of root's live input and appear once in
// the next human-triggered turn instead.
func TestUnmarkedRootPostIsQuietAndReachesNextDigest(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	agent := e.start(rec, "agent", nil)
	e.human(rec, "RESPOND: ready", "@root")
	root := e.wait(e.rootSession(rec).ID, 1)
	out, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: agent}, Mentions: []string{"@parent"}, Text: "The draft stays frozen."})
	if err != nil {
		t.Fatal(err)
	}
	if got := rootDelivery(out, rec); got != "" {
		t.Fatalf("unmarked copy woke root: %q", got)
	}
	if _, err := e.mgr.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.rootSession(rec); got.Turns != root.Turns {
		t.Fatalf("quiet copy started turn %d", got.Turns)
	}
	e.human(rec, "RESPOND: next", "@root")
	e.wait(root.ID, 2)
	turn, err := e.mgr.Store.ReadTurn(root.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if input := e.turnInput(root.ID, 2) + fmt.Sprint(turn.Result); !strings.Contains(input, "[QUIET ROOT POSTS]") || !strings.Contains(input, out.Message.ID) || !strings.Contains(input, out.Message.Text) {
		t.Fatalf("quiet copy missing from digest: %s", input)
	}
	e.human(rec, "RESPOND: later", "@root")
	e.wait(root.ID, 3)
	later, err := e.mgr.Store.ReadTurn(root.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if input := e.turnInput(root.ID, 3) + fmt.Sprint(later.Result); strings.Contains(input, out.Message.ID) {
		t.Fatalf("quiet copy repeated: %s", input)
	}
}

func TestMarkedRootPostWakesAndUnmarkedChildPostKeepsParentDelivery(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &parent)
	e.human(rec, "RESPOND: ready", "@root")
	root := e.wait(e.rootSession(rec).ID, 1)
	request := PostRequest{NeedsRoot: true, Mentions: []string{"@parent"}, Text: "Root must choose the merge order."}
	marked, err := e.svc.Post(e.ctx, request.Input(Sender{Kind: SenderSession, Room: rec.Name, Session: parent}))
	if err != nil {
		t.Fatal(err)
	}
	if !marked.Message.NeedsRoot || rootDelivery(marked, rec) != "session-queued" {
		t.Fatalf("marked post did not wake root: %+v", marked)
	}
	e.wait(root.ID, 2)
	if p := e.turnInput(root.ID, 2); !strings.Contains(p, marked.Message.ID) {
		t.Fatalf("missing marked input: %s", p)
	}
	ordinary, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: child}, Mentions: []string{"@parent"}, Text: "Parent must resolve the interface."})
	if err != nil {
		t.Fatal(err)
	}
	if rootDelivery(ordinary, rec) != "" || len(ordinary.Deliveries) != 1 || ordinary.Deliveries[0].To != parent.Address() || ordinary.Deliveries[0].Delivery != "session-queued" {
		t.Fatalf("child post changed: %+v", ordinary)
	}
}
