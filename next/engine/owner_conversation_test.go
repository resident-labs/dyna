package engine

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

var (
	feedHome = contract.Conversation{Kind: contract.ConversationFeed}
	theOwner = contract.Principal{Kind: contract.PrincipalOwner}
)

func dmHome(session contract.SessionID) contract.Conversation {
	return contract.Conversation{Kind: contract.ConversationDM, Session: session}
}

// postHome posts as a session and returns where the post landed.
func postHome(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, from contract.SessionID, req contract.PostRequest) contract.Event {
	t.Helper()
	req.Room = room
	if req.Text == "" {
		req.Text = "update"
	}
	posted, problem := s.Post(ctx, boxCaller(from), req)
	if problem != nil {
		t.Fatal(problem)
	}
	event, err := s.store.EventInRoom(ctx, room, posted.Event)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func homeOf(event contract.Event) contract.Conversation {
	return event.Body.(contract.PostCreated).Home
}

func ownerPost(t *testing.T, s *Service, ctx context.Context, req contract.PostRequest) {
	t.Helper()
	if _, problem := s.Post(ctx, theOwner, req); problem != nil {
		t.Fatal(problem)
	}
}

// With no turn owing the owner, a post goes where the owner last wrote to the session.
func TestSessionPostFollowsWhereTheOwnerLastWrote(t *testing.T) {
	s, room, worker, ctx := setup(t)
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "in private", Home: dmHome(worker)})
	if got := homeOf(postHome(t, s, ctx, room, worker, contract.PostRequest{})); got != dmHome(worker) {
		t.Fatalf("after a DM the post went to %+v", got)
	}
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "in the open", Mentions: []string{"worker"}})
	if got := homeOf(postHome(t, s, ctx, room, worker, contract.PostRequest{})); got != feedHome {
		t.Fatalf("after a feed post the post went to %+v", got)
	}
}

// A session the owner never wrote to posts to the feed.
func TestSessionPostWithoutOwnerGoesToTheFeed(t *testing.T) {
	s, room, _, worker, kid, ctx := receiveTree(t)
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "in private", Home: dmHome(worker)})
	if got := homeOf(postHome(t, s, ctx, room, kid, contract.PostRequest{})); got != feedHome {
		t.Fatalf("post went to %+v", got)
	}
}

// The reply a turn owes the owner keeps the turn's posts beside it, even after the owner writes elsewhere.
func TestSessionPostFollowsTheTurnsOwedReply(t *testing.T) {
	s, room, worker, ctx := setup(t)
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "in private", Home: dmHome(worker)})
	channelCtx, closeChannel := context.WithCancel(ctx)
	ch, problem := testChannel(t, s, channelCtx, boxCaller(worker))
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-ch).Body.(contract.TurnStart)
	closeChannel()
	for range ch {
	}
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "in the open", Mentions: []string{"worker"}})
	update := postHome(t, s, ctx, room, worker, contract.PostRequest{ToolCallID: "call"})
	if homeOf(update) != dmHome(worker) {
		t.Fatalf("mid-turn update went to %+v", homeOf(update))
	}
	if _, problem := s.TurnOutput(ctx, boxCaller(worker), contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Final: "done"}); problem != nil {
		t.Fatal(problem)
	}
	ended := eventsOf[contract.TurnEnded](t, s, ctx, room)
	// The brief also owes the parent a reply, which the update does not answer.
	replies := ended[len(ended)-1].Replies
	i := slices.IndexFunc(replies, func(reply contract.FinalReply) bool { return reply.Reply.Target == theOwner })
	if i < 0 || replies[i].Reply.Home != dmHome(worker) || replies[i].Posted != update.ID {
		t.Fatalf("replies %+v", replies)
	}
	if got := homeOf(postHome(t, s, ctx, room, worker, contract.PostRequest{})); got != feedHome {
		t.Fatalf("after the turn the post went to %+v", got)
	}
}

// Room coordination stays in the feed, and an explicit home wins.
func TestSessionPostMentionsAndExplicitHomes(t *testing.T) {
	s, room, worker, ctx := setup(t)
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "in private", Home: dmHome(worker)})
	if got := homeOf(postHome(t, s, ctx, room, worker, contract.PostRequest{Mentions: []string{"root"}})); got != feedHome {
		t.Fatalf("a mention went to %+v", got)
	}
	if got := homeOf(postHome(t, s, ctx, room, worker, contract.PostRequest{Home: feedHome})); got != feedHome {
		t.Fatalf("an explicit feed post went to %+v", got)
	}
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "in the open", Mentions: []string{"worker"}})
	if got := homeOf(postHome(t, s, ctx, room, worker, contract.PostRequest{Home: contract.Conversation{Kind: contract.ConversationDM}})); got != dmHome(worker) {
		t.Fatalf("an explicit DM post went to %+v", got)
	}
}

// A session may name its own DM only, and a DM post wakes no one.
func TestSessionDMPost(t *testing.T) {
	s, room, root, worker, kid, ctx := receiveTree(t)
	for name, home := range map[string]contract.Conversation{"another's DM": dmHome(root), "a child's DM": dmHome(kid)} {
		if _, problem := s.Post(ctx, boxCaller(worker), contract.PostRequest{Room: room, Text: "x", Home: home}); problem == nil || problem.Code != contract.ErrInvalid {
			t.Fatalf("%s: %+v", name, problem)
		}
	}
	if _, problem := s.Post(ctx, boxCaller(worker), contract.PostRequest{Room: room, Text: "x", Home: dmHome(worker), Mentions: []string{"kid"}}); problem == nil {
		t.Fatal("a DM post named a session")
	}
	post := postHome(t, s, ctx, room, worker, contract.PostRequest{Home: dmHome(worker)})
	if homeOf(post) != dmHome(worker) {
		t.Fatalf("home %+v", homeOf(post))
	}
	var n int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries WHERE seq=?", post.Seq).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a DM post made %d deliveries: %v", n, err)
	}
	page, problem := s.EventsList(ctx, theOwner, contract.EventsListRequest{Room: room, Home: &contract.Conversation{Kind: contract.ConversationDM, Session: worker}})
	if problem != nil || len(page.Events) != 1 || page.Events[0].ID != post.ID {
		t.Fatalf("the DM lists %+v, %v", page.Events, problem)
	}
}

// A frame whose input owes no reply says where posts to the owner go.
func TestFrameSaysWherePostsToTheOwnerGo(t *testing.T) {
	s, room, _, worker, kid, ctx := receiveTree(t)
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "in private", Home: dmHome(worker)})
	event := contract.Event{From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: "x"}}
	for _, c := range []struct {
		session contract.SessionID
		kind    contract.ReplyKind
		want    string
	}{
		{worker, contract.ReplyPost, " No reply is delivered. Posts to the owner go to your DM."},
		{worker, contract.ReplyNone, " No reply is expected. Posts to the owner go to your DM."},
		{kid, contract.ReplyNone, " No reply is expected. Posts to the owner go to the feed."},
	} {
		frame, _, err := s.inputFrame(ctx, c.session, event, contract.Delivery{Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: c.kind}})
		if err != nil || !strings.HasSuffix(frame, c.want) {
			t.Fatalf("%s %s: %q %v", c.session, c.kind, frame, err)
		}
	}
	frame, _, err := s.inputFrame(ctx, worker, event, contract.Delivery{Surface: contract.SurfaceDM, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: theOwner, Home: dmHome(worker)}})
	if err != nil || strings.Contains(frame, "Posts to the owner") {
		t.Fatalf("a final frame: %q %v", frame, err)
	}
	// A session that ended a turn while it waited is told whom its next text may answer.
	owed := fmt.Sprintf(`[{"kind":"final","target":{"kind":"session","session":%q}}]`, worker)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO replies_owed(session,replies) VALUES(?,?)", kid, owed)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	frame, _, err = s.inputFrame(ctx, kid, event, contract.Delivery{Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}})
	if err != nil || !strings.Contains(frame, "No reply is expected. You still owe your parent \"worker\" a reply: your final response goes to it if no job of yours is running and no child of yours is working when the turn ends.") {
		t.Fatalf("a frame with a reply owed: %q %v", frame, err)
	}
}

// An input that joins a running turn is framed for that turn: its posts line names where the turn owes the owner its reply,
// and its own reply target joins the turn's replies.
func TestASteerIsFramedForTheTurnItJoins(t *testing.T) {
	s, room, worker, ctx := setup(t)
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "in private", Home: dmHome(worker)})
	ch, problem := testChannel(t, s, ctx, boxCaller(worker))
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-ch).Body.(contract.TurnStart)
	frame := func(what string) string {
		t.Helper()
		steer, ok := (<-ch).Body.(contract.TurnSteer)
		if !ok || steer.Queue != contract.QueueSteer {
			t.Fatalf("%s: %+v", what, steer)
		}
		// Pi read it: the box reports a steer done before the turn ends, or the turn does not answer it.
		if _, problem := s.DeliveryDone(ctx, boxCaller(worker), contract.DeliveryDoneRequest{Delivery: steer.Input.Delivery}); problem != nil {
			t.Fatal(problem)
		}
		line, _, _ := strings.Cut(steer.Input.Text, "\n")
		return line
	}
	if _, problem := s.ShellJobEnded(ctx, boxCaller(worker), contract.ShellJobEndedRequest{Job: "job_1", Description: "Build", End: jobExited}); problem != nil {
		t.Fatal(problem)
	}
	if line := frame("a job's end"); !strings.HasSuffix(line, " Posts to the owner go to your DM.") {
		t.Fatalf("a job's end: %q", line)
	}
	ownerPost(t, s, ctx, contract.PostRequest{Room: room, Text: "and in the open", Mentions: []string{"worker"}})
	if line := frame("a mention"); !strings.HasSuffix(line, " Your final response goes to the owner in the feed.") {
		t.Fatalf("a mention: %q", line)
	}
	if _, problem := s.TurnOutput(ctx, boxCaller(worker), contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Final: "done"}); problem != nil {
		t.Fatal(problem)
	}
	ended := eventsOf[contract.TurnEnded](t, s, ctx, room)
	replies := ended[len(ended)-1].Replies
	for _, home := range []contract.Conversation{dmHome(worker), feedHome} {
		if !slices.ContainsFunc(replies, func(reply contract.FinalReply) bool {
			return reply.Reply.Target == theOwner && reply.Reply.Home == home
		}) {
			t.Fatalf("no reply to the owner in %+v: %+v", home, replies)
		}
	}
}
